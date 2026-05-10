// Package orbitalimager implements Phare's Orbitport Application Plugin.
//
// v0.0.1: a single RequestImagery RPC that returns a fixture image
// base64-encoded inline. The fixture is loaded from disk at NewPlugin()
// time and held in memory; if no fixture is configured, the plugin falls
// back to a small synthetic placeholder so the dev stack still boots.
//
// Future versions add tiled, resumable, disk-only transfer — see
package orbitalimager

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"os"
	"time"

	"github.com/spacecomputer-io/orbitport/plugins/pkg/utils"
	proto "github.com/spacecomputer-io/orbitport/plugins/proto/plugins"
	"golang.org/x/crypto/sha3"
)

// Plugin implements proto.OrbitalImagerPluginServer.
type Plugin struct {
	proto.UnimplementedOrbitalImagerPluginServer

	imageBytes []byte
	mimeType   string
	imageHash  []byte
	sensor     string
}

// NewPlugin creates a new orbitalimager plugin instance and pre-loads the
// fixture image so RequestImagery is a memory read at request time.
//
// When the fixture's extension is in SupportedExtensions and
// FragmentOnLoad is true, the plugin also fragments the fixture into
// fixed-size packets on disk. Fragmentation failure is logged but does
// not block boot — RequestImagery (v1) does not depend on it.
func NewPlugin() (*Plugin, error) {
	logger := utils.GetLogger("orbitport:orbitalimager")
	cfg := readFromEnv()

	imgBytes, mime, source, err := loadFixture(cfg.FixturePath)
	if err != nil {
		return nil, err
	}
	logger.Infof("Loaded imagery fixture from %s (%d bytes, %s)", source, len(imgBytes), mime)

	hash := keccak256(imgBytes)

	if cfg.FragmentOnLoad {
		fragmentFixture(logger, cfg, source, imgBytes, mime)
	}

	return &Plugin{
		imageBytes: imgBytes,
		mimeType:   mime,
		imageHash:  hash,
		sensor:     cfg.Sensor,
	}, nil
}

// fragmentFixture runs the v0.1.0 fragmenter on the loaded fixture if the
// source has a supported extension. Logs progress + failures; never returns
// an error so a broken fragment never blocks the plugin from serving v1
// RequestImagery responses.
func fragmentFixture(log *utils.Logger, cfg *orbitalImagerConfig, source string, imgBytes []byte, mime string) {
	if !IsSupportedExtension(source) {
		log.Infof("Fragmenter: source %q has unsupported extension; allowed=%v — skipping",
			source, SupportedExtensions)
		return
	}
	res, err := FragmentImage(FragmentOptions{
		SourceBytes:    imgBytes,
		SourceFilename: source,
		SourceMimeType: mime,
		OutputDir:      cfg.FragmentOutputDir,
		TilePixelSize:  cfg.FragmentTilePixelSize,
		Sensor:         cfg.Sensor,
	})
	if err != nil {
		log.Warnf("Fragmenter: failed for %s: %v", source, err)
		return
	}
	log.Infof("Fragmenter: %s → %s (%d packets, %dx%d, tile=%d)",
		source, res.ImageDir, res.Metadata.PacketCount,
		res.Metadata.ImageWidth, res.Metadata.ImageHeight, res.Metadata.TilePixelSize)
}

// RequestImagery returns the configured fixture image as base64.
//
// The request fields (lat, lon, timestamp_unix, imo) are accepted and
// logged but otherwise ignored at v0.0.1 — fixture lookup by coordinate
// arrives in v0.0.2.
func (p *Plugin) RequestImagery(_ context.Context, req *proto.ImageryRequest) (*proto.ImageryResult, error) {
	logger := utils.GetLogger("orbitport:orbitalimager:requestimagery")
	logger.Debugf("RequestImagery lat=%f lon=%f imo=%d ts=%d",
		req.GetLat(), req.GetLon(), req.GetImo(), req.GetTimestampUnix())

	return &proto.ImageryResult{
		ImageB64:   base64.StdEncoding.EncodeToString(p.imageBytes),
		MimeType:   p.mimeType,
		CapturedAt: time.Now().Unix(),
		Sensor:     p.sensor,
		ImageHash:  p.imageHash,
		Mocked:     true,
	}, nil
}

// loadFixture returns the bytes + mime of the configured fixture, or a
// synthetic placeholder if none is configured / readable.
func loadFixture(path string) (data []byte, mime string, source string, err error) {
	logger := utils.GetLogger("orbitport:orbitalimager:fixture")

	if path != "" {
		raw, rerr := os.ReadFile(path)
		if rerr == nil {
			return raw, http.DetectContentType(raw), path, nil
		}
		logger.Warnf("Fixture path %q unreadable (%v) — falling back to synthetic placeholder", path, rerr)
	} else {
		logger.Info("ORBITPORT_ORBITALIMAGER_FIXTURE_PATH unset — falling back to synthetic placeholder")
	}

	raw, gerr := generateSyntheticJPEG(256, 256)
	if gerr != nil {
		return nil, "", "", gerr
	}
	return raw, "image/jpeg", "synthetic://placeholder", nil
}

// generateSyntheticJPEG returns a small solid-colour JPEG, used as a
// placeholder when no fixture is configured. The colour is intentionally
// recognisable (warning orange) so demos make it visually obvious the
// fixture was not wired.
func generateSyntheticJPEG(w, h int) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	warningOrange := color.RGBA{R: 0xff, G: 0x8c, B: 0x1a, A: 0xff}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, warningOrange)
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 70}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// keccak256 returns the 32-byte Ethereum-style keccak digest of the input.
// We use this rather than SHA-256 because the eventual on-chain attest()
// digest binds via ECDSA.recover, which expects keccak.
func keccak256(b []byte) []byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(b)
	return h.Sum(nil)
}
