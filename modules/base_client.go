// Package modules is to warpped the API provided by each module of CSRB
//
//
package modules

import (
	"errors"
	"fmt"
	"time"

	rpcclient "github.com/tendermint/tendermint/rpc/client"

	"github.com/irisnet/service-sdk-go/codec"
	"github.com/irisnet/service-sdk-go/modules/service"
	"github.com/irisnet/service-sdk-go/std"
	sdk "github.com/irisnet/service-sdk-go/types"
	"github.com/irisnet/service-sdk-go/utils"
	"github.com/irisnet/service-sdk-go/utils/cache"
	"github.com/irisnet/service-sdk-go/utils/log"
)

const (
	concurrency       = 16
	cacheCapacity     = 100
	cacheExpirePeriod = 1 * time.Minute
	tryThreshold      = 3
	maxBatch          = 100
)

type baseClient struct {
	sdk.TmClient
	sdk.KeyManager
	logger    *log.Logger
	cfg       *sdk.ClientConfig
	marshaler codec.Marshaler
	cdc       *codec.Codec
	l         *locker

	accountQuery
	tokenQuery
	paramsQuery
}

//NewBaseClient return the baseClient for every sub modules
func NewBaseClient(cfg sdk.ClientConfig, cdc *std.Codec) sdk.BaseClient {
	//create logger
	logger := log.NewLogger(cfg.Level)

	base := baseClient{
		TmClient:  NewRPCClient(cfg.NodeURI, cdc.Amino, logger, cfg.Timeout),
		logger:    logger,
		cfg:       &cfg,
		marshaler: cdc.Marshaler,
		cdc:       cdc.Amino,
		l:         NewLocker(concurrency),
	}

	base.KeyManager = keyManager{
		keyDAO: cfg.KeyDAO,
		algo:   cfg.Algo,
	}

	c := cache.NewCache(cacheCapacity)
	base.accountQuery = accountQuery{
		Queries:    base,
		Logger:     base.Logger(),
		Cache:      c,
		cdc:        cdc.Marshaler,
		km:         base.KeyManager,
		expiration: cacheExpirePeriod,
	}

	base.tokenQuery = tokenQuery{
		q:      base,
		Logger: base.Logger(),
		Cache:  c,
	}

	base.paramsQuery = paramsQuery{
		Queries:    base,
		Logger:     base.Logger(),
		Cache:      c,
		cdc:        cdc.Marshaler,
		expiration: cacheExpirePeriod,
	}
	return &base
}

func (base *baseClient) Logger() *log.Logger {
	return base.logger
}

// Codec returns codec.
func (base *baseClient) Marshaler() codec.Marshaler {
	return base.marshaler
}

func (base *baseClient) BuildAndSend(msg []sdk.Msg, baseTx sdk.BaseTx) (sdk.ResultTx, sdk.Error) {
	txByte, ctx, err := base.buildTx(msg, baseTx)
	if err != nil {
		return sdk.ResultTx{}, err
	}

	if err := base.ValidateTxSize(len(txByte), msg); err != nil {
		return sdk.ResultTx{}, err
	}
	return base.broadcastTx(txByte, ctx.Mode(), baseTx.Simulate)
}

func (base *baseClient) SendBatch(msgs sdk.Msgs, baseTx sdk.BaseTx) (rs []sdk.ResultTx, err sdk.Error) {
	if msgs == nil || len(msgs) == 0 {
		return rs, sdk.Wrapf("must have at least one message in list")
	}

	defer sdk.CatchPanic(func(errMsg string) {
		base.Logger().Error().
			Msgf("broadcast msg failed:%s", errMsg)
	})
	//validate msg
	for _, m := range msgs {
		if err := m.ValidateBasic(); err != nil {
			return rs, sdk.Wrap(err)
		}
	}
	base.Logger().Debug().Msg("validate msg success")

	//lock the account
	base.l.Lock(baseTx.From)
	defer base.l.Unlock(baseTx.From)

	batch := maxBatch
	var tryCnt = 0

resize:
	for i, ms := range utils.SubArray(batch, msgs) {
		mss := ms.(sdk.Msgs)

	retry:
		txByte, ctx, err := base.buildTx(mss, baseTx)
		if err != nil {
			return rs, err
		}

		if err := base.ValidateTxSize(len(txByte), mss); err != nil {
			base.Logger().Warn().
				Int("msgsLength", batch).
				Msg(err.Error())

			// filter out transactions that have been sent
			msgs = msgs[i*batch:]
			// reset the maximum number of msg in each transaction
			batch = batch / 2
			_ = base.removeCache(ctx.Address())
			goto resize
		}

		res, err := base.broadcastTx(txByte, ctx.Mode(), baseTx.Simulate)
		if err != nil {
			if sdk.Code(err.Code()) == sdk.InvalidSequence {
				base.Logger().Warn().
					Str("address", ctx.Address()).
					Int("tryCnt", tryCnt).
					Msg("cached account information outdated, retrying ...")

				_ = base.removeCache(ctx.Address())
				if tryCnt++; tryCnt >= tryThreshold {
					return rs, err
				}
				goto retry
			}

			base.Logger().
				Err(err).
				Msg("broadcast transaction failed")
			return rs, err
		}
		base.Logger().Info().
			Str("txHash", res.Hash).
			Int64("height", res.Height).
			Msg("broadcast transaction success")
		rs = append(rs, res)
	}
	return rs, nil
}

func (base baseClient) QueryWithResponse(path string, data interface{}, result sdk.Response) error {
	res, err := base.Query(path, data)
	if err != nil {
		return err
	}

	if err := base.marshaler.UnmarshalJSON(res, result); err != nil {
		return err
	}

	return nil
}

func (base baseClient) Query(path string, data interface{}) ([]byte, error) {
	var bz []byte
	var err error
	if data != nil {
		bz, err = base.marshaler.MarshalJSON(data)
		if err != nil {
			return nil, err
		}
	}

	opts := rpcclient.ABCIQueryOptions{
		//Height: cliCtx.Height,
		Prove: false,
	}
	result, err := base.ABCIQueryWithOptions(path, bz, opts)
	if err != nil {
		return nil, err
	}

	resp := result.Response
	if !resp.IsOK() {
		return nil, errors.New(resp.Log)
	}

	return resp.Value, nil
}

func (base baseClient) QueryStore(key sdk.HexBytes, storeName string) (res []byte, err error) {
	path := fmt.Sprintf("/store/%s/%s", storeName, "key")
	opts := rpcclient.ABCIQueryOptions{
		//Height: cliCtx.Height,
		Prove: false,
	}

	result, err := base.ABCIQueryWithOptions(path, key, opts)
	if err != nil {
		return res, err
	}

	resp := result.Response
	if !resp.IsOK() {
		return res, errors.New(resp.Log)
	}
	return resp.Value, nil
}

func (base baseClient) QueryStoreWithProof(key sdk.HexBytes, storeName string) (res []byte, err error) {
	path := fmt.Sprintf("/store/%s/%s", storeName, "key")
	opts := rpcclient.ABCIQueryOptions{
		Prove: true,
	}

	result, err := base.ABCIQueryWithOptions(path, key, opts)
	if err != nil {
		return res, err
	}

	resp := result.Response
	if !resp.IsOK() {
		return res, errors.New(resp.Log)
	}
	return resp.Value, nil
}

func (base *baseClient) prepare(baseTx sdk.BaseTx) (*sdk.TxBuilder, error) {
	builder := sdk.NewTxBuilder(func(tx sdk.Tx) ([]byte, error) {
		return base.cdc.MarshalBinaryBare(tx)
	})
	builder.WithCodec(base.marshaler).
		WithChainID(base.cfg.ChainID).
		WithKeyManager(base.KeyManager).
		WithMode(base.cfg.Mode).
		WithSimulate(false).
		WithGas(base.cfg.Gas)

	if !baseTx.Simulate {
		addr, err := base.QueryAddress(baseTx.From, baseTx.Password)
		if err != nil {
			return nil, err
		}
		builder.WithAddress(addr.String())

		account, err := base.QueryAndRefreshAccount(addr.String())
		if err != nil {
			return nil, err
		}
		builder.WithAccountNumber(account.AccountNumber).
			WithSequence(account.Sequence).
			WithPassword(baseTx.Password)
	}

	if !baseTx.Fee.Empty() && baseTx.Fee.IsValid() {
		fees, err := base.ToMinCoin(baseTx.Fee...)
		if err != nil {
			return nil, err
		}
		builder.WithFee(fees)
	} else {
		fees, err := base.ToMinCoin(base.cfg.Fee...)
		if err != nil {
			panic(err)
		}
		builder.WithFee(fees)
	}

	if len(baseTx.Mode) > 0 {
		builder.WithMode(baseTx.Mode)
	}

	if baseTx.Simulate {
		builder.WithSimulate(baseTx.Simulate)
	}

	if baseTx.Gas > 0 {
		builder.WithGas(baseTx.Gas)
	}

	if len(baseTx.Memo) > 0 {
		builder.WithMemo(baseTx.Memo)
	}
	return builder, nil
}

func (base *baseClient) ValidateTxSize(txSize int, msgs []sdk.Msg) sdk.Error {
	var isServiceTx bool
	for _, msg := range msgs {
		if msg.Route() == service.ModuleName {
			isServiceTx = true
			break
		}
	}
	if isServiceTx {
		//var param service.Params

		//err := base.QueryParams(service.ModuleName, &param)
		//if err != nil {
		//	panic(err)
		//}

		//if uint64(txSize) > param.TxSizeLimit {
		//	return sdk.Wrapf("tx size too large, expected: <= %d, got %d", param.TxSizeLimit, txSize)
		//}
		return nil

	} else {
		if uint64(txSize) > base.cfg.MaxTxBytes {
			return sdk.Wrapf("tx size too large, expected: <= %d, got %d", base.cfg.MaxTxBytes, txSize)
		}
	}
	return nil
}

type locker struct {
	shards []chan int
	size   int
}

//NewLocker implement the function of lock, can lock resources according to conditions
func NewLocker(size int) *locker {
	shards := make([]chan int, size)
	for i := 0; i < size; i++ {
		shards[i] = make(chan int, 1)
	}
	return &locker{
		shards: shards,
		size:   size,
	}
}

func (l *locker) Lock(key string) {
	ch := l.getShard(key)
	ch <- 1
}

func (l *locker) Unlock(key string) {
	ch := l.getShard(key)
	<-ch
}

func (l *locker) getShard(key string) chan int {
	index := uint(l.indexFor(key)) % uint(l.size)
	return l.shards[index]
}

func (l *locker) indexFor(key string) uint32 {
	hash := uint32(2166136261)
	const prime32 = uint32(16777619)
	for i := 0; i < len(key); i++ {
		hash *= prime32
		hash ^= uint32(key[i])
	}
	return hash
}
