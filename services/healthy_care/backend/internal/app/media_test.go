package app

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

func TestImageValidationAndReencoding(t *testing.T) {
	im := image.NewRGBA(image.Rect(0, 0, 40, 20))
	im.Set(0, 0, color.RGBA{R: 255, A: 255})
	var b bytes.Buffer
	_ = jpeg.Encode(&b, im, nil)
	data, kind, w, h, err := normalizeImage(b.Bytes())
	if err != nil || kind != "image/jpeg" || w != 40 || h != 20 || len(data) == 0 {
		t.Fatal("valid image failed", err)
	}
	if _, _, _, _, err = normalizeImage([]byte("<script>not an image</script>")); err == nil {
		t.Fatal("active content must not be accepted as an image")
	}
}
