// v0.1.0 read-side RPC handlers for the orbitalimager plugin.
//
// These methods are pure file-system reads against the directory the
// fragmenter writes to (Plugin.fragmentDir). They never decode or re-tile
// images at request time — that work is done once at boot. Memory ceiling
// per request is one packet file.

package orbitalimager

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/spacecomputer-io/orbitport/plugins/pkg/utils"
	proto "github.com/spacecomputer-io/orbitport/plugins/proto/plugins"
)

// ListImages returns the image_ids of every fragmented image found under
// the plugin's fragment output directory. An entry is included iff the
// directory contains a readable metadata.json. The result is sorted
// lexicographically so callers see a stable order.
func (p *Plugin) ListImages(_ context.Context, _ *proto.ListImagesRequest) (*proto.ListImagesResult, error) {
	logger := utils.GetLogger("orbitport:orbitalimager:listimages")

	if p.fragmentDir == "" {
		return &proto.ListImagesResult{}, nil
	}

	entries, err := os.ReadDir(p.fragmentDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No images fragmented yet — return empty rather than error.
			return &proto.ListImagesResult{}, nil
		}
		logger.Warnf("read fragmentDir %q: %v", p.fragmentDir, err)
		return nil, status.Errorf(codes.Internal, "list images: %v", err)
	}

	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		metaPath := filepath.Join(p.fragmentDir, e.Name(), "metadata.json")
		if _, err := os.Stat(metaPath); err != nil {
			continue
		}
		ids = append(ids, e.Name())
	}
	sort.Strings(ids)

	logger.Debugf("ListImages → %d image(s)", len(ids))
	return &proto.ListImagesResult{ImageIds: ids}, nil
}

// GetImageMetadata reads metadata.json for the requested image_id and
// returns it as a typed proto message. Hex hashes on disk are decoded
// into raw bytes.
func (p *Plugin) GetImageMetadata(_ context.Context, req *proto.GetImageMetadataRequest) (*proto.ImageMetadata, error) {
	logger := utils.GetLogger("orbitport:orbitalimager:getimagemetadata")

	id, err := validateImageID(req.GetImageId())
	if err != nil {
		return nil, err
	}
	imageDir, err := p.imageDir(id)
	if err != nil {
		return nil, err
	}

	meta, err := readMetadata(imageDir)
	if err != nil {
		return nil, err
	}

	logger.Debugf("GetImageMetadata id=%s packets=%d", id, meta.PacketCount)
	return metadataToProto(meta), nil
}

// GetImagePacket returns one packet file for the requested image, base64-
// encoded so the wire shape matches ImageryResult.image_b64. Hash + width
// + height are read from metadata.json, not recomputed at request time.
func (p *Plugin) GetImagePacket(_ context.Context, req *proto.GetImagePacketRequest) (*proto.GetImagePacketResult, error) {
	logger := utils.GetLogger("orbitport:orbitalimager:getimagepacket")

	id, err := validateImageID(req.GetImageId())
	if err != nil {
		return nil, err
	}
	imageDir, err := p.imageDir(id)
	if err != nil {
		return nil, err
	}

	meta, err := readMetadata(imageDir)
	if err != nil {
		return nil, err
	}

	idx := int(req.GetPacketIndex())
	if idx < 0 || idx >= len(meta.Packets) {
		return nil, status.Errorf(codes.OutOfRange,
			"packet_index %d outside [0,%d) for image %q", idx, len(meta.Packets), id)
	}
	info := meta.Packets[idx]

	packetPath := filepath.Join(imageDir, "packets", info.Filename)
	data, err := os.ReadFile(packetPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, status.Errorf(codes.NotFound, "packet file missing: %s", info.Filename)
		}
		logger.Warnf("read packet %s: %v", packetPath, err)
		return nil, status.Errorf(codes.Internal, "read packet: %v", err)
	}

	hashBytes, _ := hex.DecodeString(info.HashKeccak256Hex)

	logger.Debugf("GetImagePacket id=%s idx=%d → %d bytes", id, idx, len(data))
	return &proto.GetImagePacketResult{
		PacketIndex: uint32(idx),
		PacketB64:   base64.StdEncoding.EncodeToString(data),
		PacketHash:  hashBytes,
		Width:       uint32(info.Width),
		Height:      uint32(info.Height),
		MimeType:    "image/png",
	}, nil
}

// validateImageID rejects empty strings and any value that could escape
// the fragment directory via path traversal. The fragmenter only ever
// produces ids from nonSafeIDChars-sanitised filenames, so a legitimate
// id can only contain [A-Za-z0-9_-]; we enforce the same allowlist here.
func validateImageID(id string) (string, error) {
	if id == "" {
		return "", status.Error(codes.InvalidArgument, "image_id is required")
	}
	if strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return "", status.Errorf(codes.InvalidArgument, "image_id %q contains path separators", id)
	}
	if nonSafeIDChars.ReplaceAllString(id, "") != id {
		return "", status.Errorf(codes.InvalidArgument, "image_id %q contains forbidden characters", id)
	}
	return id, nil
}

// imageDir returns the absolute directory for an image_id, or NotFound if
// no fragment exists. Caller must have already validated id.
func (p *Plugin) imageDir(id string) (string, error) {
	if p.fragmentDir == "" {
		return "", status.Error(codes.FailedPrecondition,
			"fragmenter is disabled (FragmentOutputDir empty)")
	}
	dir := filepath.Join(p.fragmentDir, id)
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", status.Errorf(codes.NotFound, "no fragmented image with id=%q", id)
		}
		return "", status.Errorf(codes.Internal, "stat image dir: %v", err)
	}
	return dir, nil
}

// readMetadata reads + parses the metadata.json file for an image dir.
func readMetadata(imageDir string) (*Metadata, error) {
	raw, err := os.ReadFile(filepath.Join(imageDir, "metadata.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, status.Errorf(codes.NotFound, "metadata.json missing in %s", filepath.Base(imageDir))
		}
		return nil, status.Errorf(codes.Internal, "read metadata: %v", err)
	}
	var m Metadata
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, status.Errorf(codes.Internal, "parse metadata.json: %v", err)
	}
	return &m, nil
}

// metadataToProto converts the on-disk Metadata struct (hex hashes) into
// the wire-typed ImageMetadata (raw bytes hashes). A bad hex string yields
// nil bytes rather than an error: metadata.json is server-authored, so a
// malformed hash there is a server bug, not a request-time failure.
func metadataToProto(m *Metadata) *proto.ImageMetadata {
	packets := make([]*proto.PacketInfo, 0, len(m.Packets))
	for _, p := range m.Packets {
		hashBytes, _ := hex.DecodeString(p.HashKeccak256Hex)
		packets = append(packets, &proto.PacketInfo{
			Index:      uint32(p.Index),
			Filename:   p.Filename,
			Row:        uint32(p.Row),
			Col:        uint32(p.Col),
			Width:      uint32(p.Width),
			Height:     uint32(p.Height),
			PacketHash: hashBytes,
		})
	}
	fullHash, _ := hex.DecodeString(m.FullImageHashKecHex)
	return &proto.ImageMetadata{
		Version:          m.Version,
		ImageId:          m.ImageID,
		ShipName:         m.ShipName,
		Imo:              m.IMO,
		SourceFilename:   m.SourceFilename,
		SourceMimeType:   m.SourceMimeType,
		SourceExtension:  m.SourceExtension,
		ImageWidth:       uint32(m.ImageWidth),
		ImageHeight:      uint32(m.ImageHeight),
		TilePixelSize:    uint32(m.TilePixelSize),
		TileRows:         uint32(m.TileRows),
		TileCols:         uint32(m.TileCols),
		PacketCount:      uint32(m.PacketCount),
		PacketExtension:  m.PacketExtension,
		FullImageHash:    fullHash,
		CreatedAtUnix:    m.CreatedAtUnix,
		Sensor:           m.Sensor,
		Packets:          packets,
	}
}

