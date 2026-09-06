package main

import (
	"archive/tar"
	"compress/bzip2"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/models"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

func runModels(args []string) error {
	if len(args) == 0 {
		return runModelsList(false, false, false)
	}
	switch args[0] {
	case "pull":
		if len(args) < 2 {
			return fmt.Errorf("usage: mavor models pull <name>\n\n%s", models.Summary())
		}
		return pullModel(args[1])
	case "list", "ls":
		installedOnly, verbose, asJSON := false, false, false
		for _, a := range args[1:] {
			switch a {
			case "--installed", "-i":
				installedOnly = true
			case "--verbose", "-v":
				verbose = true
			case "--json":
				asJSON = true
			default:
				return fmt.Errorf("unknown flag for 'mavor models list': %s", a)
			}
		}
		if asJSON && verbose {
			return errors.New("--json and --verbose are different renderings of the same data; pick one")
		}
		return runModelsList(installedOnly, verbose, asJSON)
	case "help", "-h", "--help":
		fmt.Printf(`usage: mavor models <command>

commands:
  list, ls            list every model mavor can download, with sizes, languages,
                      and which of them are already in the cache
      --installed,-i  restrict the listing to models already downloaded
      --verbose,-v    one block per model: speed, vocabulary biasing, GPU
      --json          the same catalog as JSON, for scripts and benchmarks
  pull <name>         download a model into the cache

%s
Examples:
  mavor models list
  mavor models list --installed
  mavor models pull whisper-base.en
  mavor models pull fastconformer-streaming
`, models.Summary())
		return nil
	default:
		return fmt.Errorf("unknown models command: %s (try 'mavor models help')", args[0])
	}
}

func pullModel(name string) error {
	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.Paths.Models, 0o755); err != nil {
		return err
	}

	cleanName := strings.TrimPrefix(name, "sherpa/")

	spec, ok := models.Lookup(cleanName)
	if !ok {
		return models.UnknownModelError(cleanName)
	}

	if spec.Engine == "sherpa" {
		targetDir := filepath.Join(cfg.Paths.Models, "sherpa", spec.TargetDir)
		if spec.Format == "raw" {
			if err := os.MkdirAll(targetDir, 0o755); err != nil {
				return fmt.Errorf("create dir %s: %w", targetDir, err)
			}
			dest := filepath.Join(targetDir, filepath.Base(spec.URL))
			if _, err := os.Stat(dest); err == nil {
				fmt.Printf("already present: %s\n", dest)
				return nil
			}
			fmt.Printf("downloading %s (%s)\nURL: %s\n", spec.Name, spec.Description, spec.URL)
			return downloadFile(spec.URL, dest)
		}

		// Archive format (tar.bz2, tar.gz, tgz, tar)
		if fi, err := os.Stat(targetDir); err == nil && fi.IsDir() {
			entries, _ := os.ReadDir(targetDir)
			if len(entries) > 0 {
				fmt.Printf("already present: %s\n", targetDir)
				return nil
			}
		}
		fmt.Printf("downloading %s (%s)\nURL: %s\n", spec.Name, spec.Description, spec.URL)
		return downloadAndExtractArchive(spec.URL, spec.Format, targetDir)
	}

	// Whisper GGML model. The file keeps the name upstream serves it under,
	// which is not the catalog name.
	dest := filepath.Join(cfg.Paths.Models, spec.Filename)
	if _, err := os.Stat(dest); err == nil {
		fmt.Printf("already present: %s\n", dest)
		return nil
	}
	fmt.Printf("downloading %s (%s)\nURL: %s\n", spec.Name, spec.Description, spec.URL)
	return downloadFile(spec.URL, dest)
}

func downloadAndExtractArchive(url, format, targetDir string) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("create dir %s: %w", targetDir, err)
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "mavor/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}

	var decompressed io.Reader
	switch format {
	case "tar.bz2", "bz2":
		decompressed = bzip2.NewReader(resp.Body)
	case "tar.gz", "tgz", "gz":
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return err
		}
		defer gz.Close()
		decompressed = gz
	case "tar":
		decompressed = resp.Body
	default:
		return fmt.Errorf("unsupported archive format: %s", format)
	}

	tarReader := tar.NewReader(decompressed)
	count := 0
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		cleanPath := filepath.Clean(header.Name)
		if cleanPath == "." || cleanPath == "/" {
			continue
		}

		// Strip top-level archive directory wrapper if present
		parts := strings.Split(filepath.ToSlash(cleanPath), "/")
		var relPath string
		if len(parts) > 1 {
			relPath = filepath.Join(parts[1:]...)
		} else {
			relPath = parts[0]
		}

		if relPath == "" || relPath == "." {
			continue
		}

		destPath := filepath.Join(targetDir, relPath)

		if header.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(destPath, 0o755); err != nil {
				return fmt.Errorf("create directory %s: %w", destPath, err)
			}
			continue
		}

		if header.Typeflag == tar.TypeReg {
			if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
				return fmt.Errorf("create dir %s: %w", filepath.Dir(destPath), err)
			}
			outFile, err := os.Create(destPath)
			if err != nil {
				return fmt.Errorf("create %s: %w", destPath, err)
			}
			if _, err := io.Copy(outFile, tarReader); err != nil {
				outFile.Close()
				return fmt.Errorf("extract %s: %w", destPath, err)
			}
			outFile.Close()
			count++
		}
	}

	if count == 0 {
		return fmt.Errorf("archive from %s contained no regular files", url)
	}

	fmt.Printf("✅ Successfully extracted %d model files to %s\n", count, targetDir)
	return nil
}

// Markers in the STATUS column.
const (
	markerActive     = "\u2605" // the model the daemon will actually load
	markerDownloaded = "\u2713" // present in the model cache
	markerAbsent     = "\u2013" // not downloaded
)

func runModelsList(installedOnly, verbose, asJSON bool) error {
	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	if asJSON {
		return listCatalogJSON(os.Stdout, cfg, installedOnly)
	}
	if verbose {
		return listCatalogVerbose(os.Stdout, cfg, installedOnly)
	}
	return listCatalog(os.Stdout, cfg, installedOnly)
}

// installedModel is what the cache holds for one name.
type installedModel struct {
	size int64
}

// cacheKey is the name a model appears under in scanInstalled's map. For a
// whisper model that is its catalog name; for a sherpa model it is the
// directory the archive unpacks into, which is not always the catalog name
// because a renamed entry pins TargetDir to keep an existing download.
func cacheKey(m models.KnownModel) string {
	if m.Engine != "sherpa" {
		return m.Name
	}
	if m.TargetDir != "" {
		return m.TargetDir
	}
	if spec, ok := models.Lookup(m.Name); ok {
		return spec.TargetDir
	}
	return m.Name
}

// cachedModels indexes the catalog by cache key, so a directory found on disk
// can be traced back to the entry that put it there.
var cachedModels = func() map[string]models.KnownModel {
	m := make(map[string]models.KnownModel, len(models.Catalog))
	for _, entry := range models.Catalog {
		m[cacheKey(entry)] = entry
	}
	return m
}()

// installedEntry reports what the cache holds for one catalog entry, or nil.
func installedEntry(installed map[string]installedModel, m models.KnownModel) *installedModel {
	if inst, ok := installed[cacheKey(m)]; ok {
		return &inst
	}
	return nil
}

// listCatalog prints every model mavor can download, with what is on disk
// marked. With installedOnly, it prints just the downloaded ones.
func listCatalog(w io.Writer, cfg config.Config, installedOnly bool) error {
	installed := scanInstalled(cfg)
	active := activeModelName(cfg)

	type row struct {
		name, engine, size, langs, stream, status string
	}
	var rows []row

	for _, m := range models.Catalog {
		got := installedEntry(installed, m)
		if installedOnly && got == nil {
			continue
		}

		status := markerAbsent
		if got != nil {
			status = markerDownloaded + " " + formatFileSize(got.size)
		}
		if active != "" && m.Name == active {
			status += "  " + markerActive
		}

		stream := "no"
		if m.Streaming {
			stream = "yes"
		}
		rows = append(rows, row{
			name:   m.Name,
			engine: m.Engine,
			size:   formatFileSize(m.DownloadSize),
			langs:  m.Languages,
			stream: stream,
			status: status,
		})
	}

	fmt.Fprintf(w, "Model cache: %s\n\n", cfg.Paths.Models)

	if len(rows) == 0 {
		fmt.Fprintln(w, "No models downloaded yet. Get one with:")
		fmt.Fprintln(w, "    mavor models pull whisper-base.en   # 141 MB, English, the default")
		fmt.Fprintln(w, "    mavor models pull whisper-tiny.en   #  74 MB, English, fastest")
		fmt.Fprintln(w, "\nRun `mavor models list` to see everything available.")
		return nil
	}

	// Column widths sized to content so the table stays readable as the
	// catalog grows.
	wName, wEngine, wSize, wLangs, wStream, wStatus := len("NAME"), len("ENGINE"), len("SIZE"), len("LANGUAGES"), len("STREAM"), len("STATUS")
	for _, r := range rows {
		wName = max(wName, len(r.name))
		wEngine = max(wEngine, len(r.engine))
		wSize = max(wSize, len(r.size))
		wLangs = max(wLangs, len(r.langs))
		wStream = max(wStream, len(r.stream))
		wStatus = max(wStatus, runeLen(r.status))
	}

	line := func(name, engine, size, langs, stream, status string) {
		fmt.Fprintln(w, strings.TrimRight(strings.Join([]string{
			padRight(name, wName), padRight(engine, wEngine), padLeft(size, wSize),
			padRight(langs, wLangs), padRight(stream, wStream), status,
		}, "  "), " "))
	}

	line("NAME", "ENGINE", "SIZE", "LANGUAGES", "STREAM", "STATUS")
	for _, r := range rows {
		line(r.name, r.engine, r.size, r.langs, r.stream, r.status)
	}

	fmt.Fprintf(w, "\n%s active   %s downloaded   %s not downloaded\n", markerActive, markerDownloaded, markerAbsent)
	if !installedOnly {
		fmt.Fprintln(w, "SIZE is the download; sherpa archives expand to roughly twice that on disk.")
		fmt.Fprintln(w, "Download one with `mavor models pull <name>`.")
	}

	// Anything in the cache that the catalog does not know about — a
	// hand-placed or hand-converted model — still belongs in the listing.
	if extras := unknownInstalled(installed); len(extras) > 0 {
		fmt.Fprintln(w, "\nAlso in the cache, not from the catalog:")
		for _, name := range extras {
			fmt.Fprintf(w, "  %-24s %-9s  %s\n", name,
				formatFileSize(installed[name].size),
				describeSherpaModel(name, filepath.Join(cfg.Paths.Models, "sherpa", name)))
		}
	}
	return nil
}

// listCatalogVerbose prints one block per model with every property the
// catalog carries. The table view has room for what you scan; this has room
// for the caveats — which biasing a model can take, what GPU support depends
// on, and whether a speed figure was measured or estimated.
func listCatalogVerbose(w io.Writer, cfg config.Config, installedOnly bool) error {
	installed := scanInstalled(cfg)
	active := activeModelName(cfg)

	fmt.Fprintf(w, "Model cache: %s\n", cfg.Paths.Models)

	shown := 0
	for _, m := range models.Catalog {
		got := installedEntry(installed, m)
		if installedOnly && got == nil {
			continue
		}
		shown++

		state := markerAbsent + " not downloaded"
		if got != nil {
			state = fmt.Sprintf("%s downloaded (%s)", markerDownloaded, formatFileSize(got.size))
		}
		if active != "" && m.Name == active {
			state += "   " + markerActive + " active"
		}

		fmt.Fprintf(w, "\n%s\n", m.Name)
		fmt.Fprintf(w, "  %s\n", m.Description)
		field(w, "engine", engineDetail(m.Engine))
		field(w, "download", formatFileSize(m.DownloadSize))
		field(w, "languages", m.Languages)
		field(w, "streaming", streamingDetail(m.Streaming))
		field(w, "speed", speedDetail(m))
		field(w, "vocabulary", m.Vocabulary)
		field(w, "gpu", gpuDetail(m.Engine))
		field(w, "status", state)
		field(w, "source", m.URL)
	}

	if shown == 0 {
		fmt.Fprintln(w, "\nNo models downloaded yet. Get one with `mavor models pull whisper-base.en`.")
		return nil
	}

	fmt.Fprint(w, "\n"+verboseFootnotes)
	return nil
}

// catalogJSON is the machine-readable form of the listing: the same rows the
// table renders, in the shape a benchmark harness or a script wants. It is a
// single object rather than JSON Lines because a consumer almost always wants
// the whole catalog at once, and because ModelDir belongs to the listing as a
// whole and not to any row in it.
type catalogJSON struct {
	ModelDir string             `json:"model_dir"`
	Models   []catalogModelJSON `json:"models"`
}

// catalogModelJSON is one model. Every field the catalog carries is here,
// including the ones only --verbose renders, so a consumer never has to parse
// the human tables to recover a property.
type catalogModelJSON struct {
	Name        string `json:"name"`
	Engine      string `json:"engine"`
	Family      string `json:"family"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Filename    string `json:"filename,omitempty"`
	DownloadS   int64  `json:"download_size"`
	Languages   string `json:"languages"`
	Streaming   bool   `json:"streaming"`
	Transducer  bool   `json:"transducer"`
	Vocabulary  string `json:"vocabulary"`

	// Speed is the relative tier and MeasuredRTF the benchmark, kept as
	// separate fields so a consumer cannot mistake one for the other. The
	// estimated flag says outright which it is looking at: a tier is an
	// architectural guess, and reporting it as a measurement is exactly the
	// error this output exists to prevent.
	Speed       string  `json:"speed"`
	MeasuredRTF float64 `json:"measured_rtf,omitempty"`
	SpeedIsEst  bool    `json:"speed_is_estimated"`

	// Installed reports the cache, not the catalog: whether this model is on
	// disk under any of its names, how big it is there, and whether it is the
	// one the daemon would load right now.
	Installed     bool  `json:"installed"`
	InstalledSize int64 `json:"installed_size,omitempty"`
	Active        bool  `json:"active"`
}

// listCatalogJSON writes the catalog as JSON. It shares scanInstalled and
// activeModelName with the table renderers so the three views cannot disagree
// about what is on disk.
func listCatalogJSON(w io.Writer, cfg config.Config, installedOnly bool) error {
	installed := scanInstalled(cfg)
	active := activeModelName(cfg)

	out := catalogJSON{ModelDir: cfg.Paths.Models, Models: []catalogModelJSON{}}
	for _, m := range models.Catalog {
		got := installedEntry(installed, m)
		if installedOnly && got == nil {
			continue
		}

		row := catalogModelJSON{
			Name:        m.Name,
			Engine:      m.Engine,
			Family:      m.Family,
			Description: m.Description,
			URL:         m.URL,
			Filename:    m.Filename,
			DownloadS:   m.DownloadSize,
			Languages:   m.Languages,
			Streaming:   m.Streaming,
			Transducer:  m.Transducer,
			Vocabulary:  m.Vocabulary,
			Speed:       m.Speed,
			MeasuredRTF: m.MeasuredRTF,
			SpeedIsEst:  m.MeasuredRTF == 0,
			Installed:   got != nil,
			Active:      active != "" && m.Name == active,
		}
		if got != nil {
			row.InstalledSize = got.size
		}
		out.Models = append(out.Models, row)
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// verboseFootnotes carries the caveats the per-model fields cannot: what the
// speed tier is and is not, and that GPU support is a property of the build.
const verboseFootnotes = `speed is a relative tier across this catalog, estimated from architecture and
parameter count — not a measurement. The few figures marked "measured" come
from docs/reports/: whisper-cli at 4 threads on a 12-core x86_64 CPU over 20s
of speech. Your numbers will differ; an RTF below 1.0 is faster than real time.

gpu support depends on the build you are running, not on the model. Run
` + "`mavor doctor`" + ` to see what yours can actually use.
`

func field(w io.Writer, name, value string) {
	fmt.Fprintf(w, "  %-11s %s\n", name, value)
}

func engineDetail(engine string) string {
	if engine == "sherpa" {
		return "sherpa (in-process sherpa-onnx, CGO)"
	}
	return "whisper (whisper-cli subprocess or whisper-server)"
}

func streamingDetail(streaming bool) string {
	if streaming {
		return "yes — decodes incrementally while you speak"
	}
	return "no — transcribes once you stop speaking"
}

// speedDetail prefers a measured figure and says so, rather than letting an
// estimated tier read like a benchmark.
func speedDetail(m models.KnownModel) string {
	if m.MeasuredRTF > 0 {
		return fmt.Sprintf("%s · measured RTF %.3f (%.1fx real time)",
			m.Speed, m.MeasuredRTF, 1/m.MeasuredRTF)
	}
	return m.Speed + " (relative tier, not measured)"
}

func gpuDetail(engine string) string {
	if engine == "sherpa" {
		return "none in practice — the bundled ONNX Runtime is a CPU-only build"
	}
	return "used automatically when the whisper.cpp build has a GPU backend"
}

// The status markers are multi-byte, so columns are padded by rune count.
// fmt's %-*s pads by bytes and would misalign every row containing one.
func runeLen(s string) int { return len([]rune(s)) }

func padRight(s string, w int) string {
	if n := w - runeLen(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

func padLeft(s string, w int) string {
	if n := w - runeLen(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

// scanInstalled reports what the model cache actually holds, keyed by the
// name a user would type: whisper models by their ggml-<name>.bin stem, sherpa
// models by their directory name.
func scanInstalled(cfg config.Config) map[string]installedModel {
	found := map[string]installedModel{}

	entries, err := os.ReadDir(cfg.Paths.Models)
	if err != nil {
		return found
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && speech.IsWhisperModelFile(name) {
			info, err := e.Info()
			if err != nil {
				continue
			}
			// Keyed by catalog name, not by the file's own stem: the two
			// differ for every whisper model, and the listing looks models
			// up by the name it prints. A file with no catalog entry keeps
			// its stem so a hand-placed model still shows up.
			key, _ := speech.WhisperCatalogName(name)
			found[key] = installedModel{size: info.Size()}
			continue
		}
		if e.IsDir() && name != "sherpa" {
			dir := filepath.Join(cfg.Paths.Models, name)
			if containsModelFiles(dir) {
				found[name] = installedModel{size: dirSize(dir)}
			}
		}
	}

	sherpaBase := filepath.Join(cfg.Paths.Models, "sherpa")
	if subs, err := os.ReadDir(sherpaBase); err == nil {
		for _, se := range subs {
			if !se.IsDir() {
				continue
			}
			dir := filepath.Join(sherpaBase, se.Name())
			if entries, _ := os.ReadDir(dir); len(entries) == 0 {
				continue
			}
			found[se.Name()] = installedModel{size: dirSize(dir)}
		}
	}
	return found
}

// activeModelName is the model the daemon would load with the current config.
// There is one key that says so: the runtime follows from the model, not the
// other way round.
func activeModelName(cfg config.Config) string {
	return cfg.Model
}

// unknownInstalled lists cached models with no catalog entry, sorted so the
// output is stable.
func unknownInstalled(installed map[string]installedModel) []string {
	var extras []string
	for name := range installed {
		if _, known := cachedModels[name]; !known {
			extras = append(extras, name)
		}
	}
	sort.Strings(extras)
	return extras
}

func formatFileSize(bytes int64) string {
	mb := float64(bytes) / (1024 * 1024)
	if mb >= 1024 {
		return fmt.Sprintf("%.2f GB", mb/1024)
	}
	return fmt.Sprintf("%.1f MB", mb)
}

func dirSize(path string) int64 {
	var total int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func describeSherpaModel(name, dirPath string) string {
	if km, ok := cachedModels[name]; ok {
		if km.Family != "" {
			return fmt.Sprintf("Sherpa ONNX / %s", km.Family)
		}
		return fmt.Sprintf("Sherpa ONNX / %s", km.Description)
	}
	// Try inspecting directory files
	entries, err := os.ReadDir(dirPath)
	if err == nil {
		for _, e := range entries {
			lower := strings.ToLower(e.Name())
			if strings.Contains(lower, "moonshine") {
				return "Sherpa ONNX / Moonshine"
			}
			if strings.Contains(lower, "sensevoice") || strings.Contains(lower, "sense-voice") {
				return "Sherpa ONNX / SenseVoice"
			}
			if strings.Contains(lower, "zipformer") {
				return "Sherpa ONNX / Zipformer"
			}
			if strings.Contains(lower, "parakeet") {
				return "Sherpa ONNX / NeMo Parakeet"
			}
			if strings.Contains(lower, "canary") {
				return "Sherpa ONNX / NeMo Canary"
			}
			if strings.Contains(lower, "paraformer") {
				return "Sherpa ONNX / Paraformer"
			}
		}
	}
	return "Sherpa ONNX"
}

func containsModelFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			name := strings.ToLower(e.Name())
			if strings.HasSuffix(name, ".onnx") || strings.HasSuffix(name, ".bin") || strings.HasSuffix(name, ".pt") {
				return true
			}
		}
	}
	return false
}

func downloadFile(url, dest string) error {
	tmp := dest + ".part"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "mavor/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}
