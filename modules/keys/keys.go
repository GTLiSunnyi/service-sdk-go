package keys

import (
	"github.com/irisnet/service-sdk-go/codec"
	"github.com/irisnet/service-sdk-go/codec/types"
	sdk "github.com/irisnet/service-sdk-go/types"
)

type keysClient struct {
	sdk.KeyManager
	codec.Marshaler
}

func NewClient(km sdk.KeyManager, cdc codec.Marshaler) KeyI {
	return keysClient{
		KeyManager: km,
		Marshaler:  cdc}
}

func (b keysClient) RegisterCodec(cdc *codec.Codec) {
	registerCodec(cdc)
}

func (b keysClient) RegisterInterfaceTypes(registry types.InterfaceRegistry) {
	registry.RegisterImplementations(
		(*sdk.Msg)(nil),
		&MsgSend{},
		&MsgMultiSend{},
	)
}

func (k keysClient) Add(name, password string) (string, string, sdk.Error) {
	address, mnemonic, err := k.Insert(name, password)
	return address, mnemonic, sdk.Wrap(err)
}

func (k keysClient) Recover(name, password, mnemonic string) (string, sdk.Error) {
	address, err := k.KeyManager.Recover(name, password, mnemonic)
	return address, sdk.Wrap(err)
}

func (k keysClient) Import(name, password, privKeyArmor string) (string, sdk.Error) {
	address, err := k.KeyManager.Import(name, password, privKeyArmor)
	return address, sdk.Wrap(err)
}

func (k keysClient) Export(name, password string) (string, sdk.Error) {
	keystore, err := k.KeyManager.Export(name, password)
	return keystore, sdk.Wrap(err)
}

func (k keysClient) Delete(name, password string) sdk.Error {
	err := k.KeyManager.Delete(name, password)
	return sdk.Wrap(err)
}

func (k keysClient) Show(name, password string) (string, sdk.Error) {
	address, err := k.KeyManager.Find(name, password)
	if err != nil {
		return "", sdk.Wrap(err)
	}
	return address.String(), nil
}