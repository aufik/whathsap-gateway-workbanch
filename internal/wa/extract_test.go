package wa

import (
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func TestExtractMessageBubbleAndMultiBubbleFormat(t *testing.T) {
	// 1. Image message inside Ephemeral container
	msgImg := &waE2E.Message{
		EphemeralMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{
				ImageMessage: &waE2E.ImageMessage{
					Caption:  proto.String("*Pasar Tradisional Kota Medan Tahun 2022*"),
					Mimetype: proto.String("image/png"),
				},
			},
		},
	}
	gotImg := ExtractMessageBubble(msgImg)
	if !strings.Contains(gotImg, "Pasar Tradisional") || !strings.Contains(gotImg, "[Berkas] image/png") {
		t.Fatalf("unexpected image extraction: %q", gotImg)
	}

	// 2. Location message
	msgLoc := &waE2E.Message{
		LocationMessage: &waE2E.LocationMessage{
			DegreesLatitude:  proto.Float64(3.5578396),
			DegreesLongitude: proto.Float64(98.7260964),
		},
	}
	gotLoc := ExtractMessageBubble(msgLoc)
	if !strings.Contains(gotLoc, "[Koordinat] 3.5578396, 98.7260964") {
		t.Fatalf("unexpected location extraction: %q", gotLoc)
	}

	// 3. Multi-bubble formatting matches Excel convention [1] ... [2] ...
	combined := formatCollectedBubbles([]collectedBubble{
		{text: "Alamat UPT Pengembangan Benih Hortikultura"},
		{text: gotLoc},
	})
	if !strings.HasPrefix(combined, "[1] Alamat UPT") || !strings.Contains(combined, "[2] [Koordinat]") {
		t.Fatalf("unexpected multi-bubble format: %q", combined)
	}
}
