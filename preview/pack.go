package preview

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/charmbracelet/x/ansi"
)

// pack.go turns an authoring directory (preview.yaml + frame files) into the
// single preview.scp bundle. Errors name the offending file/row/sequence so
// authors can fix art without reading source. The packer pads short
// rows/frames but NEVER truncates: an oversize row is the author's call to
// make, not ours.

// ManifestName is the manifest file every authoring directory carries.
const ManifestName = "preview.yaml"

// manifest is the authoring manifest: frames in playback order, each with its
// file and duration. Looping is implicit — there is nothing to configure.
type manifest struct {
	Frames []manifestFrame `yaml:"frames"`
}

type manifestFrame struct {
	File string `yaml:"file"`
	MS   int    `yaml:"ms"`
}

// Pack reads dir's manifest and frame files and returns the bundle bytes.
func Pack(dir string) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m manifest
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestName, err)
	}
	if len(m.Frames) < MinFrames || len(m.Frames) > MaxFrames {
		return nil, fmt.Errorf("%s lists %d frames; want %d..%d", ManifestName, len(m.Frames), MinFrames, MaxFrames)
	}

	b := bundleJSON{V: Version, Frames: make([]frameJSON, len(m.Frames))}
	for i, entry := range m.Frames {
		if entry.File == "" {
			return nil, fmt.Errorf("%s frames[%d]: missing file", ManifestName, i)
		}
		if entry.MS == 0 {
			return nil, fmt.Errorf("%s frames[%d] (%s): missing ms", ManifestName, i, entry.File)
		}
		if entry.MS < 0 {
			return nil, fmt.Errorf("%s frames[%d] (%s): negative ms", ManifestName, i, entry.File)
		}
		lines, err := packFrame(dir, entry.File)
		if err != nil {
			return nil, err
		}
		b.Frames[i] = frameJSON{MS: clampMS(entry.MS), Lines: lines}
	}

	out, err := json.Marshal(b)
	if err != nil {
		return nil, fmt.Errorf("encode bundle: %w", err)
	}
	if len(out) > MaxBundleBytes {
		return nil, fmt.Errorf("packed bundle is %d bytes; limit %d", len(out), MaxBundleBytes)
	}
	return out, nil
}

// packFrame reads, validates, and pads one frame file to exactly Rows×Cols.
func packFrame(dir, name string) ([]string, error) {
	if filepath.IsAbs(name) || strings.Contains(name, "..") {
		return nil, fmt.Errorf("frame file %q: name must be relative to the preview directory", name)
	}
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil, fmt.Errorf("read frame: %w", err)
	}
	src := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(src) > Rows {
		return nil, fmt.Errorf("%s has %d rows; a frame is at most %d", name, len(src), Rows)
	}
	lines := make([]string, Rows)
	for r := range lines {
		if r >= len(src) {
			lines[r] = strings.Repeat(" ", Cols)
			continue
		}
		line := src[r]
		if err := validateText(line); err != nil {
			return nil, fmt.Errorf("%s row %d: %w", name, r+1, err)
		}
		if w := ansi.StringWidth(line); w > Cols {
			return nil, fmt.Errorf("%s row %d is %d cells; the art block is %d — trim the art, the packer never truncates", name, r+1, w, Cols)
		}
		lines[r] = pad(line)
	}
	return lines, nil
}
