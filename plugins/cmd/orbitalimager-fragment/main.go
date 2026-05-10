// orbitalimager-fragment is a thin CLI wrapper around
// orbitalimager.FragmentImage for ad-hoc local runs.
//
//	go run ./cmd/orbitalimager-fragment -in <image> [-out <dir>] [-tile 256]
//
// Output layout:
//
//	<out>/<image_id>/
//	├── metadata.json
//	└── packets/0000.png …
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	oi "github.com/spacecomputer-io/orbitport/plugins/pkg/plugin/orbitalimager"
)

func main() {
	in := flag.String("in", "", "path to source image (.png/.jpg/.jpeg/.gif)")
	out := flag.String("out", "./.orbitalimager-demo/images", "output base dir")
	tile := flag.Int("tile", 256, "tile pixel size")
	sensor := flag.String("sensor", "phare-mock-1", "sensor identifier stored in metadata")
	flag.Parse()

	if *in == "" {
		fmt.Fprintln(os.Stderr, "usage: orbitalimager-fragment -in <image> [-out <dir>] [-tile 256]")
		os.Exit(2)
	}
	if !oi.IsSupportedExtension(*in) {
		fmt.Fprintf(os.Stderr, "unsupported extension; allowed=%v\n", oi.SupportedExtensions)
		os.Exit(2)
	}

	src, err := os.ReadFile(*in)
	if err != nil {
		die("read %s: %v", *in, err)
	}
	abs, err := filepath.Abs(*out)
	if err != nil {
		die("resolve out: %v", err)
	}

	res, err := oi.FragmentImage(oi.FragmentOptions{
		SourceBytes:    src,
		SourceFilename: *in,
		SourceMimeType: http.DetectContentType(src),
		OutputDir:      abs,
		TilePixelSize:  *tile,
		Sensor:         *sensor,
	})
	if err != nil {
		die("fragment: %v", err)
	}

	m := res.Metadata
	fmt.Printf("imageDir   %s\n", res.ImageDir)
	fmt.Printf("metadata   %s\n", res.MetadataPath)
	fmt.Printf("ship       %s  (imo=%d)\n", m.ShipName, m.IMO)
	fmt.Printf("source     %s  %s  %dx%d\n", m.SourceFilename, m.SourceMimeType, m.ImageWidth, m.ImageHeight)
	fmt.Printf("grid       %d rows × %d cols  =  %d packets  (tile=%d)\n",
		m.TileRows, m.TileCols, m.PacketCount, m.TilePixelSize)
	fmt.Printf("hash       %s\n", m.FullImageHashKecHex)
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
