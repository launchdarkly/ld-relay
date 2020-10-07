package browser

import "encoding/base64"

const transparent1PixelImgBase64 = "R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7="

// Transparent1PixelImageData is the response data for the fake-image endpoint that browsers can use to send events.
// It is exported for use in test code.
var Transparent1PixelImageData []byte = makePixelImageData() //nolint:gochecknoglobals

func makePixelImageData() []byte {
	data, _ := base64.StdEncoding.DecodeString(transparent1PixelImgBase64)
	return data
}
