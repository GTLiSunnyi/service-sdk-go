package std

import (
	"github.com/irisnet/service-sdk-go/codec"
	"github.com/irisnet/service-sdk-go/codec/types"
)

// Codec defines the application-level codec. This codec contains all the
// required module-specific codecs that are to be provided upon initialization.
type Codec struct {
	codec.Marshaler

	// Keep reference to the amino codec to allow backwards compatibility along
	// with type, and interface registration.
	Amino *codec.Codec

	anyUnpacker types.AnyUnpacker
}

func NewAppCodec(amino *codec.Codec, anyUnpacker types.AnyUnpacker) *Codec {
	return &Codec{Marshaler: codec.NewHybridCodec(amino, anyUnpacker), Amino: amino, anyUnpacker: anyUnpacker}
}
