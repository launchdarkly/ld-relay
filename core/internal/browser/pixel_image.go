package browser

import "encoding/base64"

const Transparent1PixelImgBase64 = "R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7="

var Transparent1PixelImageData []byte = makePixelImageData()

func makePixelImageData() []byte {
	data, _ := base64.StdEncoding.DecodeString(Transparent1PixelImgBase64)
	return data
}
