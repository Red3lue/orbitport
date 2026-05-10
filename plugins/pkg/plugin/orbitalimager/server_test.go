package orbitalimager

import (
	"bytes"
	"context"
	"encoding/base64"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	proto "github.com/spacecomputer-io/orbitport/plugins/proto/plugins"
)

// setup spins up an in-process gRPC server hosting *Plugin, fragments two
// synthetic images into a temp dir, and returns a connected client. All
// tests share the same fixture set so we exercise multi-image listing.
func setup(t *testing.T) (proto.OrbitalImagerPluginClient, string) {
	t.Helper()

	dir := t.TempDir()
	for _, name := range []string{"alpha-ship.png", "bravo-ship.png"} {
		if _, err := FragmentImage(FragmentOptions{
			SourceBytes:    makePNG(t, 300, 200),
			SourceFilename: name,
			SourceMimeType: "image/png",
			OutputDir:      dir,
			TilePixelSize:  128,
			Sensor:         "phare-mock-1",
		}); err != nil {
			t.Fatalf("seed fragment %s: %v", name, err)
		}
	}

	plug := &Plugin{fragmentDir: dir}

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	proto.RegisterOrbitalImagerPluginServer(srv, plug)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return proto.NewOrbitalImagerPluginClient(conn), dir
}

func TestListImages(t *testing.T) {
	cli, _ := setup(t)
	res, err := cli.ListImages(context.Background(), &proto.ListImagesRequest{})
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	want := []string{"alpha-ship", "bravo-ship"}
	if len(res.GetImageIds()) != len(want) {
		t.Fatalf("ids: got %v, want %v", res.GetImageIds(), want)
	}
	for i, id := range res.GetImageIds() {
		if id != want[i] {
			t.Errorf("id[%d] = %q, want %q (sort order broken)", i, id, want[i])
		}
	}
}

func TestListImages_EmptyDir(t *testing.T) {
	plug := &Plugin{fragmentDir: t.TempDir()}
	res, err := plug.ListImages(context.Background(), &proto.ListImagesRequest{})
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	if len(res.GetImageIds()) != 0 {
		t.Errorf("empty dir: got %v, want []", res.GetImageIds())
	}
}

func TestGetImageMetadata(t *testing.T) {
	cli, _ := setup(t)
	res, err := cli.GetImageMetadata(context.Background(),
		&proto.GetImageMetadataRequest{ImageId: "alpha-ship"})
	if err != nil {
		t.Fatalf("GetImageMetadata: %v", err)
	}

	if res.GetImageId() != "alpha-ship" {
		t.Errorf("ImageId = %q, want alpha-ship", res.GetImageId())
	}
	if res.GetShipName() != "Alpha Ship" {
		t.Errorf("ShipName = %q, want %q", res.GetShipName(), "Alpha Ship")
	}
	if res.GetVersion() != "0.1.0" {
		t.Errorf("Version = %q, want 0.1.0", res.GetVersion())
	}
	// 300x200 with tile=128 → 3 cols × 2 rows = 6 packets.
	if got, want := res.GetPacketCount(), uint32(6); got != want {
		t.Errorf("PacketCount = %d, want %d", got, want)
	}
	if got, want := res.GetTileRows(), uint32(2); got != want {
		t.Errorf("TileRows = %d, want %d", got, want)
	}
	if got, want := res.GetTileCols(), uint32(3); got != want {
		t.Errorf("TileCols = %d, want %d", got, want)
	}
	if len(res.GetFullImageHash()) != 32 {
		t.Errorf("FullImageHash len = %d, want 32 (raw keccak256 bytes)", len(res.GetFullImageHash()))
	}
	if len(res.GetPackets()) != 6 {
		t.Fatalf("packets returned = %d, want 6", len(res.GetPackets()))
	}
	for i, p := range res.GetPackets() {
		if p.GetIndex() != uint32(i) {
			t.Errorf("packet[%d].Index = %d, want %d", i, p.GetIndex(), i)
		}
		if len(p.GetPacketHash()) != 32 {
			t.Errorf("packet[%d].PacketHash len = %d, want 32", i, len(p.GetPacketHash()))
		}
	}
}

func TestGetImageMetadata_NotFound(t *testing.T) {
	cli, _ := setup(t)
	_, err := cli.GetImageMetadata(context.Background(),
		&proto.GetImageMetadataRequest{ImageId: "no-such-ship"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("err code = %v, want NotFound", status.Code(err))
	}
}

func TestGetImageMetadata_BadID(t *testing.T) {
	cli, _ := setup(t)
	cases := []string{"", "../etc", "a/b", `a\b`, "with space", "name;rm"}
	for _, id := range cases {
		_, err := cli.GetImageMetadata(context.Background(),
			&proto.GetImageMetadataRequest{ImageId: id})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("id=%q: code = %v, want InvalidArgument", id, status.Code(err))
		}
	}
}

func TestGetImagePacket(t *testing.T) {
	cli, dir := setup(t)
	res, err := cli.GetImagePacket(context.Background(), &proto.GetImagePacketRequest{
		ImageId: "alpha-ship", PacketIndex: 0,
	})
	if err != nil {
		t.Fatalf("GetImagePacket: %v", err)
	}

	if res.GetPacketIndex() != 0 {
		t.Errorf("PacketIndex = %d, want 0", res.GetPacketIndex())
	}
	if res.GetMimeType() != "image/png" {
		t.Errorf("MimeType = %q, want image/png", res.GetMimeType())
	}
	if got, want := res.GetWidth(), uint32(128); got != want {
		t.Errorf("Width = %d, want %d", got, want)
	}
	if got, want := res.GetHeight(), uint32(128); got != want {
		t.Errorf("Height = %d, want %d", got, want)
	}
	if len(res.GetPacketHash()) != 32 {
		t.Errorf("PacketHash len = %d, want 32", len(res.GetPacketHash()))
	}

	// Decoded base64 must match the file on disk byte-for-byte.
	decoded, err := base64.StdEncoding.DecodeString(res.GetPacketB64())
	if err != nil {
		t.Fatalf("decode b64: %v", err)
	}
	want, err := os.ReadFile(filepath.Join(dir, "alpha-ship", "packets", "0000.png"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, want) {
		t.Errorf("decoded packet (%d B) != on-disk file (%d B)", len(decoded), len(want))
	}
	if _, err := png.Decode(bytes.NewReader(decoded)); err != nil {
		t.Errorf("decoded base64 is not a valid PNG: %v", err)
	}
}

func TestGetImagePacket_EdgeTileDimensions(t *testing.T) {
	cli, _ := setup(t)
	// Image 300x200, tile=128 → last col idx 2 has width 300-256=44;
	// last row idx 1 has height 200-128=72. Packet index 5 is bottom-right.
	res, err := cli.GetImagePacket(context.Background(), &proto.GetImagePacketRequest{
		ImageId: "alpha-ship", PacketIndex: 5,
	})
	if err != nil {
		t.Fatalf("GetImagePacket: %v", err)
	}
	if got, want := res.GetWidth(), uint32(44); got != want {
		t.Errorf("edge tile Width = %d, want %d", got, want)
	}
	if got, want := res.GetHeight(), uint32(72); got != want {
		t.Errorf("edge tile Height = %d, want %d", got, want)
	}
}

func TestGetImagePacket_OutOfRange(t *testing.T) {
	cli, _ := setup(t)
	_, err := cli.GetImagePacket(context.Background(), &proto.GetImagePacketRequest{
		ImageId: "alpha-ship", PacketIndex: 999,
	})
	if status.Code(err) != codes.OutOfRange {
		t.Errorf("code = %v, want OutOfRange", status.Code(err))
	}
}
