package sirenhead

// Ren'Py media extraction (image / audio / video).
//
// Unlike text — where the CLI rewrites bytes inside a source file —
// media is a **file-replacement** workflow:
//
//	extract  walk the game dir, classify every asset, write one JSON
//	         sidecar per asset under <project>/<kind>/<sub-media>/
//	inject   read each sidecar; where `target` names a replacement
//	         file, copy that file over the original
//
// An empty target means "leave the original alone", so an extract →
// inject round-trip with no targets is byte-identical by construction.
//
// Sub-media defaults (docs/shell-layer/04-media.md § 4.3), overridable
// from `gallate.yaml` or `--engine.<media>.{includes,excludes}`:
//
//	image → background, portrait, cg, ui
//	audio → voice, bgm, sfx
//	video → cutscene, opening, ending

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Recognized asset extensions.
var (
	imageExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true}
	audioExts = map[string]bool{".wav": true, ".ogg": true, ".mp3": true, ".opus": true}
	videoExts = map[string]bool{".ogv": true, ".webm": true, ".mp4": true, ".avi": true, ".mkv": true}
)

// Declared sub-media identifiers, per media kind.
var (
	imageSubMedia = []string{"background", "portrait", "cg", "ui"}
	audioSubMedia = []string{"voice", "bgm", "sfx"}
	videoSubMedia = []string{"cutscene", "opening", "ending"}
)

// mediaClass is a classified asset.
type mediaClass struct {
	sourcePath string // relative to the game root
	abs        string
	subMedia   string
	kind       string // image | audio | video
	size       int64
	hash       string
}

// id is the asset's stable, engine-independent identifier:
// `media/<kind>/<sub-media>/<flattened-path>`.
func (a mediaClass) id() string {
	flat := strings.ReplaceAll(filepath.ToSlash(a.sourcePath), "/", "_")
	return "media/" + a.kind + "/" + a.subMedia + "/" + flat
}

// Classification patterns.
var (
	imageNameHints = []struct {
		sub string
		re  *regexp.Regexp
	}{
		{"background", regexp.MustCompile(`(?i)(Background|^BG|_BG|back_)`)},
		{"cg", regexp.MustCompile(`(?i)(CG|Ending|Event\d)`)},
	}
	audioNameHints = []struct {
		sub string
		re  *regexp.Regexp
	}{
		{"voice", regexp.MustCompile(`(?i)(voice|line)`)},
		{"bgm", regexp.MustCompile(`(?i)(music|bgm|theme)`)},
	}
	videoNameHints = []struct {
		sub string
		re  *regexp.Regexp
	}{
		{"ending", regexp.MustCompile(`(?i)(Ending|ED_|EndRoll)`)},
		{"opening", regexp.MustCompile(`(?i)(Opening|^OP|OP_)`)},
	}

	imgDeclRE   = regexp.MustCompile(`(?m)^\s*image\s+\w+\s*=\s*["']([^"']+)["']`)
	sceneShowRE = regexp.MustCompile(`(?m)^\s*(?:scene|show)\s+(\w+)`)
	voiceRE     = regexp.MustCompile(`(?m)^\s*voice\s+["']([^"']+)["']`)
	playMusicRE = regexp.MustCompile(`(?m)^\s*play\s+music\s+["']([^"']+)["']`)
	playSoundRE = regexp.MustCompile(`(?m)^\s*play\s+sound\s+["']([^"']+)["']`)
	movieRE     = regexp.MustCompile(`(?m)^\s*\$?\s*renpy\.movie_cutscene\s*\(\s*["']([^"']+)["']`)
	_           = sceneShowRE
)

// scriptRefs holds the media references found in the game's `.rpy` files.
type scriptRefs struct {
	imageRefs map[string]bool
	voiceRefs map[string]bool
	musicRefs map[string]bool
	soundRefs map[string]bool
	videoRefs map[string]bool
}

// scanScriptRefs scans every `.rpy` file for media references, so
// classification can use the script's own statements rather than
// filename guessing alone.
func scanScriptRefs(gameRoot string, ignore []ignoreRule) (*scriptRefs, error) {
	refs := &scriptRefs{
		imageRefs: map[string]bool{}, voiceRefs: map[string]bool{},
		musicRefs: map[string]bool{}, soundRefs: map[string]bool{}, videoRefs: map[string]bool{},
	}
	files, err := sortedWalk(gameRoot)
	if err != nil {
		return nil, err
	}
	for _, rel := range files {
		if !strings.HasSuffix(strings.ToLower(rel), ".rpy") || ignored(ignore, rel, false) {
			continue
		}
		raw, rerr := os.ReadFile(filepath.Join(gameRoot, filepath.FromSlash(rel)))
		if rerr != nil {
			continue
		}
		text, _, ok := decodeWithBOM(raw)
		if !ok {
			continue
		}
		collect := func(re *regexp.Regexp, dst map[string]bool) {
			for _, m := range re.FindAllStringSubmatch(text, -1) {
				dst[m[1]] = true
			}
		}
		collect(imgDeclRE, refs.imageRefs)
		collect(voiceRE, refs.voiceRefs)
		collect(playMusicRE, refs.musicRefs)
		collect(playSoundRE, refs.soundRefs)
		collect(movieRE, refs.videoRefs)
	}
	return refs, nil
}

// classifyImage returns one of background / portrait / cg / ui.
func classifyImage(rel string, refs *scriptRefs) string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, p := range parts {
		if p == "gui" {
			return "ui" // UI textures always live in gui/
		}
	}
	name := filepath.Base(rel)
	for _, h := range imageNameHints {
		if h.re.MatchString(name) {
			return h.sub
		}
	}
	// Referenced from the script ⇒ story art that isn't formally
	// declared; otherwise treat it as background art.
	if refs.imageRefs[rel] {
		return "portrait"
	}
	for ref := range refs.imageRefs {
		if strings.HasSuffix(rel, ref) {
			return "portrait"
		}
	}
	return "background"
}

// classifyAudio returns one of voice / bgm / sfx.
func classifyAudio(rel string, refs *scriptRefs) string {
	switch {
	case refs.voiceRefs[rel]:
		return "voice"
	case refs.musicRefs[rel]:
		return "bgm"
	case refs.soundRefs[rel]:
		return "sfx"
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, folder := range parts {
		switch folder {
		case "voice":
			return "voice"
		case "music":
			return "bgm"
		case "sfx":
			return "sfx"
		}
	}
	name := filepath.Base(rel)
	for _, h := range audioNameHints {
		if h.re.MatchString(name) {
			return h.sub
		}
	}
	return "sfx" // most common for unreferenced audio
}

// classifyVideo returns one of cutscene / opening / ending.
func classifyVideo(rel string, refs *scriptRefs) string {
	name := filepath.Base(rel)
	for _, h := range videoNameHints {
		if h.re.MatchString(name) {
			return h.sub
		}
	}
	return "cutscene"
}

// classifyAll walks the game root and classifies every media asset.
//
// Assets that exist on disk but are not referenced anywhere are still
// picked up: they may be cut content the user wants localized anyway.
func classifyAll(gameRoot string, refs *scriptRefs, ignore []ignoreRule) ([]mediaClass, []mediaClass, []mediaClass, error) {
	var images, audios, videos []mediaClass
	files, err := sortedWalk(gameRoot)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, rel := range files {
		if ignored(ignore, rel, false) {
			continue
		}
		ext := strings.ToLower(filepath.Ext(rel))
		abs := filepath.Join(gameRoot, filepath.FromSlash(rel))
		var kind, sub string
		switch {
		case imageExts[ext]:
			kind, sub = "image", classifyImage(rel, refs)
		case audioExts[ext]:
			kind, sub = "audio", classifyAudio(rel, refs)
		case videoExts[ext]:
			kind, sub = "video", classifyVideo(rel, refs)
		default:
			continue // .rpy/.rpyc/.save/… are not localizable resources
		}
		hash, herr := sha256File(abs)
		if herr != nil {
			return nil, nil, nil, herr
		}
		var size int64
		if st, serr := os.Stat(abs); serr == nil {
			size = st.Size()
		}
		asset := mediaClass{
			sourcePath: filepath.ToSlash(rel), abs: abs,
			subMedia: sub, kind: kind, size: size, hash: hash,
		}
		switch kind {
		case "image":
			images = append(images, asset)
		case "audio":
			audios = append(audios, asset)
		case "video":
			videos = append(videos, asset)
		}
	}
	return images, audios, videos, nil
}

// filterSubMedia applies docs/shell-layer/04-media.md § 4.3:
// `includes` is a whitelist, `excludes` is subtractive, and an exclude
// that is not in the includes set is a misconfiguration (error, not a
// silent no-op).
func filterSubMedia(assets []mediaClass, includes, excludes map[string]bool, kind string) ([]mediaClass, error) {
	if includes == nil && excludes == nil {
		return assets, nil
	}
	effective := includes
	if effective == nil {
		effective = map[string]bool{}
		for _, a := range assets {
			effective[a.subMedia] = true
		}
	}
	var out []mediaClass
	for _, a := range assets {
		if effective[a.subMedia] {
			out = append(out, a)
		}
	}
	if len(excludes) > 0 {
		var unknown []string
		for sub := range excludes {
			if !effective[sub] {
				unknown = append(unknown, sub)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return nil, exitErr(ExitProjectConfig,
				"engine."+kind+".excludes references sub-media not in the includes set: "+
					strings.Join(unknown, ", "))
		}
		var kept []mediaClass
		for _, a := range out {
			if !excludes[a.subMedia] {
				kept = append(kept, a)
			}
		}
		out = kept
	}
	return out, nil
}

// validateSubMediaNames rejects sub-media identifiers the engine has
// not declared (docs/shell-layer/04-media.md § 4.3 rule 2).
func validateSubMediaNames(kind string, includes, excludes map[string]bool) error {
	declared := map[string]bool{}
	switch kind {
	case "image":
		for _, s := range imageSubMedia {
			declared[s] = true
		}
	case "audio":
		for _, s := range audioSubMedia {
			declared[s] = true
		}
	case "video":
		for _, s := range videoSubMedia {
			declared[s] = true
		}
	}
	for set := range []map[string]bool{includes, excludes} {
		_ = set
	}
	for _, set := range []map[string]bool{includes, excludes} {
		for sub := range set {
			if !declared[sub] {
				return exitErr(ExitProjectConfig,
					"sub-media identifier "+
						"\""+sub+"\" is not declared by this engine for "+kind)
			}
		}
	}
	return nil
}

// sidecarPath is the project path of one asset's JSON sidecar.
func sidecarPath(kind, subMedia, sourcePath string) string {
	return filepath.ToSlash(filepath.Join(kind, subMedia, sourcePath+".json"))
}

// sidecarDoc renders a media sidecar document.
func sidecarDoc(a mediaClass) *orderedMap {
	entry := &orderedMap{}
	entry.Set("id", a.id())
	entry.Set("source", a.sourcePath)
	entry.Set("target", "")
	entry.Set("state", "initial")
	md := &orderedMap{}
	md.Set("media_kind", a.kind)
	md.Set("sub_media", a.subMedia)
	md.Set("source_path", a.sourcePath)
	md.Set("size", a.size)
	md.Set("hash", a.hash)
	entry.Set("metadata", md)

	doc := &orderedMap{}
	doc.Set("format", UnitFormat)
	doc.Set("version", UnitVersion)
	doc.Set("source", "original")
	doc.Set("target", "translation")
	doc.Set("entries", []any{entry})
	return doc
}

// mediaInventory is the result of a media extract.
type mediaInventory struct {
	images []mediaClass
	audios []mediaClass
	videos []mediaClass
}

// extractMedia classifies and writes sidecars for the enabled media
// kinds, returning the inventory and the sidecar project paths per kind.
//
// `enabled` gates each kind: a disabled kind is neither classified nor
// written, and does not appear in `.meta.json`.
func extractMedia(
	gameRoot, projectRoot string,
	filters map[string][2]map[string]bool,
	enabled map[string]bool,
	ignore []ignoreRule,
	rep *Reporter,
	dryRun bool,
) (*mediaInventory, map[string][]string, error) {
	inv := &mediaInventory{}
	written := map[string][]string{"image": {}, "audio": {}, "video": {}}
	anyEnabled := false
	for _, kind := range []string{"image", "audio", "video"} {
		if enabled[kind] {
			anyEnabled = true
		}
	}
	if !anyEnabled {
		return inv, written, nil
	}

	refs, err := scanScriptRefs(gameRoot, ignore)
	if err != nil {
		return nil, nil, err
	}
	images, audios, videos, err := classifyAll(gameRoot, refs, ignore)
	if err != nil {
		return nil, nil, err
	}

	write := func(kind string, assets []mediaClass) error {
		for _, a := range assets {
			rel := sidecarPath(kind, a.subMedia, a.sourcePath)
			if !dryRun {
				abs := filepath.Join(projectRoot, filepath.FromSlash(rel))
				if err := atomicWriteJSON(abs, sidecarDoc(a)); err != nil {
					return exitErr(ExitOutputFailure, err.Error())
				}
			}
			written[kind] = append(written[kind], rel)
			if !dryRun {
				rep.file("write", rel)
			}
		}
		return nil
	}

	if enabled["image"] {
		inc, exc := filters["image"][0], filters["image"][1]
		if err := validateSubMediaNames("image", inc, exc); err != nil {
			return nil, nil, err
		}
		inv.images, err = filterSubMedia(images, inc, exc, "image")
		if err != nil {
			return nil, nil, err
		}
		if err := write("image", inv.images); err != nil {
			return nil, nil, err
		}
	}
	if enabled["audio"] {
		inc, exc := filters["audio"][0], filters["audio"][1]
		if err := validateSubMediaNames("audio", inc, exc); err != nil {
			return nil, nil, err
		}
		inv.audios, err = filterSubMedia(audios, inc, exc, "audio")
		if err != nil {
			return nil, nil, err
		}
		if err := write("audio", inv.audios); err != nil {
			return nil, nil, err
		}
	}
	if enabled["video"] {
		inc, exc := filters["video"][0], filters["video"][1]
		if err := validateSubMediaNames("video", inc, exc); err != nil {
			return nil, nil, err
		}
		inv.videos, err = filterSubMedia(videos, inc, exc, "video")
		if err != nil {
			return nil, nil, err
		}
		if err := write("video", inv.videos); err != nil {
			return nil, nil, err
		}
	}
	return inv, written, nil
}

// mediaMetaEntry builds the `.meta.json` entry for one media sidecar.
//
// Per docs/shell-layer/13 § 13.4.1 `size` and `hash` describe the
// **project file** (the sidecar), not the game asset: the mapping model
// is about project files. The asset's own size and hash live under
// `extensions.<cli-id>` so nothing is lost.
func mediaMetaEntry(a mediaClass, rel string, projectRoot string) *orderedMap {
	abs := filepath.Join(projectRoot, filepath.FromSlash(rel))
	var size int64
	if st, err := os.Stat(abs); err == nil {
		size = st.Size()
	}
	hash, err := sha256File(abs)
	if err != nil {
		hash = ""
	}
	m := &orderedMap{}
	m.Set("project", rel)
	m.Set("source", a.sourcePath)
	m.Set("type", a.kind)
	cli := &orderedMap{}
	cli.Set("id", CLIID)
	cli.Set("version", CLIVersion)
	m.Set("cli", cli)
	eng := &orderedMap{}
	eng.Set("id", EngineID)
	m.Set("engine", eng)
	m.Set("sub_media", a.subMedia)
	m.Set("size", size)
	if hash != "" {
		m.Set("hash", hash)
	}
	ext := &orderedMap{}
	ours := &orderedMap{}
	ours.Set("renpy_kind", a.kind)
	ours.Set("sub_media", a.subMedia)
	ours.Set("media_byte_count", a.size)
	ours.Set("media_hash", a.hash)
	ext.Set(CLIID, ours)
	m.Set("extensions", ext)
	return m
}

// mediaInjectResult counts what inject copied.
type mediaInjectResult struct {
	images, audios, videos int
	bytes                  int64
	failed                 int
}

// injectMedia copies user-supplied replacement files over the game's
// media files, guided by the sidecars' `target` fields.
//
// Nothing is copied when `target` is empty, which is what makes the
// no-translation round-trip byte-identical.
func injectMedia(
	projectRoot, gameRoot string,
	inPlace bool,
	outputRoot string,
	ignore []ignoreRule,
	rep *Reporter,
) (*mediaInjectResult, error) {
	res := &mediaInjectResult{}
	for _, kind := range []string{"image", "audio", "video"} {
		base := filepath.Join(projectRoot, kind)
		if !dirExists(base) {
			continue
		}
		sidecars, err := sortedWalk(base)
		if err != nil {
			return nil, err
		}
		for _, rel := range sidecars {
			if !strings.HasSuffix(rel, ".json") {
				continue
			}
			rel = filepath.ToSlash(filepath.Join(kind, rel))
			sidecarAbs := filepath.Join(projectRoot, filepath.FromSlash(rel))
			copied, n, failed, err := injectSidecar(sidecarAbs, gameRoot, inPlace, outputRoot, ignore, rep)
			if err != nil {
				return nil, err
			}
			switch {
			case copied && kind == "image":
				res.images++
			case copied && kind == "audio":
				res.audios++
			case copied && kind == "video":
				res.videos++
			}
			if failed {
				res.failed++
			}
			res.bytes += n
		}
	}
	return res, nil
}

// injectSidecar applies one sidecar.
func injectSidecar(sidecarAbs, gameRoot string, inPlace bool, outputRoot string, ignore []ignoreRule, rep *Reporter) (copied bool, bytes int64, failed bool, err error) {
	data, rerr := os.ReadFile(sidecarAbs)
	if rerr != nil {
		return false, 0, false, rerr
	}
	var doc map[string]any
	if err := unmarshalJSON(data, &doc); err != nil {
		return false, 0, true, nil // malformed sidecar: not fatal, never silent
	}
	entries, _ := doc["entries"].([]any)
	if len(entries) == 0 {
		return false, 0, false, nil
	}
	entry, ok := entries[0].(map[string]any)
	if !ok {
		return false, 0, false, nil
	}
	target, _ := entry["target"].(string)
	if target == "" {
		return false, 0, false, nil
	}
	targetPath := resolvePath(target, filepath.Dir(sidecarAbs))
	if !fileExists(targetPath) {
		rep.warn("MEDIA_TARGET_MISSING",
			"replacement file named by a sidecar does not exist", targetPath)
		return false, 0, true, nil
	}
	md := asMap(entry["metadata"])
	if md == nil {
		return false, 0, true, nil
	}
	sourceRel, _ := md["source_path"].(string)
	if sourceRel == "" {
		return false, 0, true, nil
	}
	if ignored(ignore, sourceRel, false) {
		rep.file("skip", sourceRel)
		return false, 0, false, nil
	}
	sourceAbs := filepath.Join(gameRoot, filepath.FromSlash(sourceRel))
	if !fileExists(sourceAbs) {
		// A mapping entry pointing at a source that no longer exists is
		// reported, never silently ignored (docs/shell-layer/13 § 13.8).
		rep.warn("SOURCE_MISSING", "media source no longer exists", sourceAbs)
		return false, 0, true, nil
	}
	dest := sourceAbs
	if !inPlace && outputRoot != "" {
		dest = filepath.Join(outputRoot, filepath.FromSlash(sourceRel))
	}
	if err := copyFileAtomic(targetPath, dest); err != nil {
		return false, 0, false, err
	}
	st, _ := os.Stat(targetPath)
	if st != nil {
		bytes = st.Size()
	}
	rep.file("modify", sourceRel)
	return true, bytes, false, nil
}

// copyFileAtomic copies src over dst atomically, verifying the bytes it
// wrote match the source before replacing the destination.
func copyFileAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return exitErr(ExitInjectionFailure, err.Error())
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return exitErr(ExitOutputFailure, err.Error())
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".*.tmp")
	if err != nil {
		return exitErr(ExitOutputFailure, err.Error())
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		cleanup()
		return exitErr(ExitOutputFailure, err.Error())
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return exitErr(ExitOutputFailure, err.Error())
	}
	expect, err := os.ReadFile(src)
	if err != nil {
		cleanup()
		return exitErr(ExitOutputFailure, err.Error())
	}
	got, err := os.ReadFile(tmpName)
	if err != nil {
		cleanup()
		return exitErr(ExitOutputFailure, err.Error())
	}
	if len(got) != len(expect) || string(got) != string(expect) {
		cleanup()
		return exitErr(ExitOutputFailure, "copy verification failed for "+filepath.Base(dst))
	}
	if err := os.Rename(tmpName, dst); err != nil {
		cleanup()
		return exitErr(ExitOutputFailure, err.Error())
	}
	return nil
}
