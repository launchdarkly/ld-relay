package wire

import (
	"fmt"
	"math"

	"github.com/vmihailenco/msgpack/v5"
	"github.com/vmihailenco/msgpack/v5/msgpcode"
)

// PayloadVersion is a base payload version: the version at which a content change applies,
// or the version a cursor advances to.
//
// The type exists because the schemas type these three fields as "number" rather than
// "integer": put-object.version, delete-object.version and payload-transferred.version. The
// same specification types the protocol version in hello and welcome as "integer", so the
// inconsistency reads as an oversight rather than an allowance -- but until it is corrected,
// a float is a legal encoding and a decoder that refuses one fails the whole connection on
// its first object.
//
// That is not hypothetical. The development mock, written independently from the same
// schemas, declares the field as a float64 and sends it that way. No LaunchDarkly server
// exists to compare against yet, so the mock is the only evidence available, and it is
// evidence that a competent reading of the schema produces floats.
//
// Delete this type once the schemas say "integer", and make the fields plain ints.
type PayloadVersion int

// DecodeMsgpack accepts an integer or a floating-point encoding.
func (v *PayloadVersion) DecodeMsgpack(dec *msgpack.Decoder) error {
	code, err := dec.PeekCode()
	if err != nil {
		return err
	}
	if msgpcode.IsFixedNum(code) || code == msgpcode.Int8 || code == msgpcode.Int16 ||
		code == msgpcode.Int32 || code == msgpcode.Int64 || code == msgpcode.Uint8 ||
		code == msgpcode.Uint16 || code == msgpcode.Uint32 || code == msgpcode.Uint64 {
		n, err := dec.DecodeInt64()
		if err != nil {
			return err
		}
		*v = PayloadVersion(n)
		return nil
	}

	n, err := dec.DecodeFloat64()
	if err != nil {
		return err
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) {
		return fmt.Errorf("megastream: payload version %v is not a whole number", n)
	}
	*v = PayloadVersion(n)
	return nil
}
