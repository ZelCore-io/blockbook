package firo

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"

	"github.com/juju/errors"
	"github.com/martinboehm/btcd/chaincfg/chainhash"
	"github.com/martinboehm/btcd/wire"
	"github.com/martinboehm/btcutil/base58"
	"github.com/martinboehm/btcutil/chaincfg"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/btc"
)

const (
	OpZeroCoinMint             = 0xc1
	OpZeroCoinSpend            = 0xc2
	OpSigmaMint                = 0xc3
	OpSigmaSpend               = 0xc4
	OpLelantusMint             = 0xc5
	OpLelantusJMint            = 0xc6
	OpLelantusJoinSplit        = 0xc7
	OpZeroCoinToSigmaRemint    = 0xc8
	OpLelantusJoinSplitPayload = 0xc9
	OpSparkMint                = 0xd1
	OpSparkSMint               = 0xd2
	OpSparkSpend               = 0xd3
	OpExchangeAddr             = 0xe0

	MainnetMagic wire.BitcoinNet = 0xe3d9fef1
	TestnetMagic wire.BitcoinNet = 0xcffcbeea
	RegtestMagic wire.BitcoinNet = 0xfabfb5da

	GenesisBlockTime                  = 1414776286
	SwitchToMTPBlockHeader            = 1544443200
	SwitchToProgPowBlockHeaderTestnet = 1630069200
	SwitchToProgPowBlockHeaderMainnet = 1635228000
	MTPL                              = 64

	SpendTxID = "0000000000000000000000000000000000000000000000000000000000000000"

	TransactionQuorumCommitmentType = 6
	TransactionSparkType            = 9
	TransactionSparkV2Type          = 11

	// MaxMoney is MAX_MONEY of firod (src/amount.h), 21.4 million FIRO in satoshis
	MaxMoney = 21400000 * 100000000
)

// Base58 prefixes of exchange addresses (EXCHANGE_PUBKEY_ADDRESS in
// src/chainparams.cpp of firod v0.14.18.0: CMainParams "EXX", CTestNetParams
// "EXT", CRegTestParams "EXR"). Unlike the 1-byte P2PKH/P2SH prefixes they
// are 3 bytes long, so btcutil cannot decode them.
var (
	mainnetExchangeAddrPrefix = []byte{0x01, 0xb9, 0xbb}
	testnetExchangeAddrPrefix = []byte{0x01, 0xb9, 0xb1}
	regtestExchangeAddrPrefix = []byte{0x01, 0xb9, 0xac}
)

var (
	MainNetParams chaincfg.Params
	TestNetParams chaincfg.Params
	RegtestParams chaincfg.Params
)

func init() {
	// mainnet
	MainNetParams = chaincfg.MainNetParams
	MainNetParams.Net = MainnetMagic

	MainNetParams.AddressMagicLen = 1
	MainNetParams.PubKeyHashAddrID = []byte{0x52}
	MainNetParams.ScriptHashAddrID = []byte{0x07}

	// testnet
	TestNetParams = chaincfg.TestNet3Params
	TestNetParams.Net = TestnetMagic

	TestNetParams.AddressMagicLen = 1
	TestNetParams.PubKeyHashAddrID = []byte{0x41}
	TestNetParams.ScriptHashAddrID = []byte{0xb2}

	// regtest
	RegtestParams = chaincfg.RegressionNetParams
	RegtestParams.Net = RegtestMagic
}

// FiroParser handle
type FiroParser struct {
	*btc.BitcoinLikeParser
	exchangeAddrPrefix              []byte
	baseOutputScriptToAddressesFunc btc.OutputScriptToAddressesFunc
}

// NewFiroParser returns new FiroParser instance
func NewFiroParser(params *chaincfg.Params, c *btc.Configuration) *FiroParser {
	p := &FiroParser{
		BitcoinLikeParser: btc.NewBitcoinLikeParser(params, c),
	}
	switch params.Net {
	case MainnetMagic:
		p.exchangeAddrPrefix = mainnetExchangeAddrPrefix
	case TestnetMagic:
		p.exchangeAddrPrefix = testnetExchangeAddrPrefix
	case RegtestMagic:
		p.exchangeAddrPrefix = regtestExchangeAddrPrefix
	}
	p.baseOutputScriptToAddressesFunc = p.OutputScriptToAddressesFunc
	p.OutputScriptToAddressesFunc = p.outputScriptToAddresses
	return p
}

// exchangeScriptLen is the length of OP_EXCHANGEADDR + a P2PKH script
const exchangeScriptLen = 1 + 25

// isExchangeScript reports whether script is OP_EXCHANGEADDR OP_DUP OP_HASH160
// <20 bytes> OP_EQUALVERIFY OP_CHECKSIG, firod's TX_EXCHANGEADDRESS template
// (src/script/standard.cpp)
func isExchangeScript(script []byte) bool {
	return len(script) == exchangeScriptLen && script[0] == OpExchangeAddr &&
		script[1] == 0x76 && script[2] == 0xa9 && script[3] == 0x14 &&
		script[24] == 0x88 && script[25] == 0xac
}

// outputScriptToAddresses adds exchange addresses ("EX...") to the bitcoin script decoding
func (p *FiroParser) outputScriptToAddresses(script []byte) ([]string, bool, error) {
	if isExchangeScript(script) && len(p.exchangeAddrPrefix) > 0 {
		return []string{p.encodeExchangeAddress(script[4:24])}, true, nil
	}
	return p.baseOutputScriptToAddressesFunc(script)
}

func base58Checksum(b []byte) []byte {
	h := sha256.Sum256(b)
	h = sha256.Sum256(h[:])
	return h[:4]
}

func (p *FiroParser) encodeExchangeAddress(hash160 []byte) string {
	b := make([]byte, 0, len(p.exchangeAddrPrefix)+len(hash160)+4)
	b = append(b, p.exchangeAddrPrefix...)
	b = append(b, hash160...)
	b = append(b, base58Checksum(b)...)
	return base58.Encode(b)
}

// exchangeAddressToOutputScript returns the output script of an exchange
// address, or nil if address is not an exchange address of this network
func (p *FiroParser) exchangeAddressToOutputScript(address string) []byte {
	pl := len(p.exchangeAddrPrefix)
	b := base58.Decode(address)
	if pl == 0 || len(b) != pl+20+4 || !bytes.Equal(b[:pl], p.exchangeAddrPrefix) ||
		!bytes.Equal(base58Checksum(b[:pl+20]), b[pl+20:]) {
		return nil
	}
	script := make([]byte, 0, exchangeScriptLen)
	script = append(script, OpExchangeAddr, 0x76, 0xa9, 0x14)
	script = append(script, b[pl:pl+20]...)
	return append(script, 0x88, 0xac)
}

// GetAddrDescFromAddress returns internal address representation (descriptor) of given address
func (p *FiroParser) GetAddrDescFromAddress(address string) (bchain.AddressDescriptor, error) {
	if script := p.exchangeAddressToOutputScript(address); script != nil {
		return script, nil
	}
	return p.BitcoinLikeParser.GetAddrDescFromAddress(address)
}

// GetChainParams contains network parameters for the main Firo network,
// the regression test Firo network, the test Firo network and
// the simulation test Firo network, in this order
func GetChainParams(chain string) *chaincfg.Params {
	if !chaincfg.IsRegistered(&MainNetParams) {
		err := chaincfg.Register(&MainNetParams)
		if err == nil {
			err = chaincfg.Register(&TestNetParams)
		}
		if err == nil {
			err = chaincfg.Register(&RegtestParams)
		}
		if err != nil {
			panic(err)
		}
	}
	switch chain {
	case "test":
		return &TestNetParams
	case "regtest":
		return &RegtestParams
	default:
		return &MainNetParams
	}
}

// GetAddressesFromAddrDesc returns addresses for given address descriptor with flag if the addresses are searchable
func (p *FiroParser) GetAddressesFromAddrDesc(addrDesc bchain.AddressDescriptor) ([]string, bool, error) {

	if len(addrDesc) > 0 {
		switch addrDesc[0] {
		case OpZeroCoinMint:
			return []string{"Zeromint"}, false, nil
		case OpZeroCoinSpend:
			return []string{"Zerospend"}, false, nil
		case OpSigmaMint:
			return []string{"Sigmamint"}, false, nil
		case OpSigmaSpend:
			return []string{"Sigmaspend"}, false, nil
		case OpLelantusMint:
			return []string{"LelantusMint"}, false, nil
		case OpLelantusJMint:
			return []string{"LelantusJMint"}, false, nil
		case OpLelantusJoinSplit:
			return []string{"LelantusJoinSplit"}, false, nil
		case OpLelantusJoinSplitPayload:
			return []string{"LelantusJoinSplit"}, false, nil
		case OpSparkMint:
			return []string{"SparkMint"}, false, nil
		case OpSparkSMint:
			return []string{"SparkSMint"}, false, nil
		case OpSparkSpend:
			return []string{"SparkSpend"}, false, nil
		}
	}

	return p.OutputScriptToAddressesFunc(addrDesc)
}

// PackTx packs transaction to byte array using protobuf
func (p *FiroParser) PackTx(tx *bchain.Tx, height uint32, blockTime int64) ([]byte, error) {
	return p.BaseParser.PackTx(tx, height, blockTime)
}

// UnpackTx unpacks transaction from protobuf byte array. Transactions stored
// in the tx cache by earlier versions of parseFiroTx carry private spend
// inputs as coinbase inputs, parseFiroTx normalizes them.
func (p *FiroParser) UnpackTx(buf []byte) (*bchain.Tx, uint32, error) {
	tx, height, err := p.BaseParser.UnpackTx(buf)
	if err != nil {
		return nil, 0, err
	}
	if err = p.parseFiroTx(tx); err != nil {
		return nil, 0, err
	}
	return tx, height, nil
}

// TxFromFiroMsgTx converts bitcoin wire Tx to bchain.Tx
func (p *FiroParser) TxFromFiroMsgTx(t *FiroMsgTx, parseAddresses bool) bchain.Tx {
	btx := p.TxFromMsgTx(&t.MsgTx, parseAddresses)

	// NOTE: wire.MsgTx.TxHash() doesn't include extra
	btx.Txid = t.TxHash().String()

	return btx
}

// ParseBlock parses raw block to our Block struct
func (p *FiroParser) ParseBlock(b []byte) (*bchain.Block, error) {
	reader := bytes.NewReader(b)

	// parse standard block header first
	header, err := parseBlockHeader(reader)
	if err != nil {
		return nil, err
	}

	// then ProgPow or MTP header
	if isProgPow(header, p.Params.Net == TestnetMagic) {
		progPowHeader := ProgPowBlockHeader{}

		// header
		err = binary.Read(reader, binary.LittleEndian, &progPowHeader)
		if err != nil {
			return nil, err
		}
	} else {
		if isMTP(header) {
			mtpHeader := MTPBlockHeader{}
			mtpHashDataRoot := MTPHashDataRoot{}

			// header
			err = binary.Read(reader, binary.LittleEndian, &mtpHeader)
			if err != nil {
				return nil, err
			}

			// hash data root
			err = binary.Read(reader, binary.LittleEndian, &mtpHashDataRoot)
			if err != nil {
				return nil, err
			}

			isAllZero := true
			for i := 0; i < 16; i++ {
				if mtpHashDataRoot.HashRootMTP[i] != 0 {
					isAllZero = false
					break
				}
			}

			if !isAllZero {
				// hash data
				mtpHashData := MTPHashData{}
				err = binary.Read(reader, binary.LittleEndian, &mtpHashData)
				if err != nil {
					return nil, err
				}

				// proof
				for i := 0; i < MTPL*3; i++ {
					var numberProofBlocks uint8

					err = binary.Read(reader, binary.LittleEndian, &numberProofBlocks)
					if err != nil {
						return nil, err
					}

					for j := uint8(0); j < numberProofBlocks; j++ {
						var mtpData [16]uint8

						err = binary.Read(reader, binary.LittleEndian, mtpData[:])
						if err != nil {
							return nil, err
						}
					}
				}
			}
		}
	}

	// parse txs
	ntx, err := wire.ReadVarInt(reader, 0)
	if err != nil {
		return nil, err
	}

	txs := make([]bchain.Tx, ntx)

	for i := uint64(0); i < ntx; i++ {
		tx, err := decodeFiroMsgTx(reader)
		if err != nil {
			return nil, err
		}

		btx := p.TxFromFiroMsgTx(tx, false)

		if err = p.parseFiroTx(&btx); err != nil {
			return nil, err
		}

		txs[i] = btx
	}

	return &bchain.Block{
		BlockHeader: bchain.BlockHeader{
			Prev: header.PrevBlock.String(), // needed for fork detection when parsing raw blocks
			Size: len(b),
			Time: header.Timestamp.Unix(),
		},
		Txs: txs,
	}, nil
}

// decodeFiroMsgTx decodes one transaction, including the extra payload of
// special (version 3) transactions, from r
func decodeFiroMsgTx(r io.ReadSeeker) (*FiroMsgTx, error) {
	// read version and seek back
	var version uint32
	if err := binary.Read(r, binary.LittleEndian, &version); err != nil {
		return nil, err
	}
	if _, err := r.Seek(-4, io.SeekCurrent); err != nil {
		return nil, err
	}

	txVersion := version & 0xffff
	txType := (version >> 16) & 0xffff

	enc := wire.WitnessEncoding

	// transaction quorum commitment could not be parsed with witness flag
	if txVersion == 3 && txType == TransactionQuorumCommitmentType {
		enc = wire.BaseEncoding
	}

	tx := &FiroMsgTx{}
	if err := tx.FiroDecode(r, 0, enc); err != nil {
		return nil, err
	}
	return tx, nil
}

// ParseTx parses a raw transaction (the mempool path). The inherited bitcoin
// parser ignores the extra payload of special transactions, which gives them
// a wrong txid and fails on quorum commitments, so decode it like ParseBlock does.
func (p *FiroParser) ParseTx(b []byte) (*bchain.Tx, error) {
	tx, err := decodeFiroMsgTx(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	btx := p.TxFromFiroMsgTx(tx, true)
	if err = p.parseFiroTx(&btx); err != nil {
		return nil, err
	}
	btx.Hex = hex.EncodeToString(b)
	return &btx, nil
}

// ParseTxFromJson parses JSON message containing transaction and returns Tx struct
func (p *FiroParser) ParseTxFromJson(msg json.RawMessage) (*bchain.Tx, error) {
	var tx bchain.Tx
	err := json.Unmarshal(msg, &tx)
	if err != nil {
		return nil, err
	}

	for i := range tx.Vout {
		vout := &tx.Vout[i]
		// convert vout.JsonValue to big.Int and clear it, it is only temporary value used for unmarshal
		vout.ValueSat, err = p.AmountToBigInt(vout.JsonValue)
		if err != nil {
			return nil, err
		}
		vout.JsonValue = ""
	}

	p.parseFiroTx(&tx)

	return &tx, nil
}

// isPrivateSpendScript reports whether the hex scriptSig of an input with a
// null previous outpoint is a Zerocoin, Sigma, Lelantus or Spark spend, using
// the opcodes firod checks in CTransaction::IsCoinBase and CTxIn::Is*Spend
// (src/primitives/transaction.h and transaction.cpp)
func isPrivateSpendScript(scriptHex string) bool {
	if len(scriptHex) < 2 {
		return false
	}
	b, err := hex.DecodeString(scriptHex[:2])
	if err != nil {
		return false
	}
	switch b[0] {
	case OpZeroCoinSpend, OpSigmaSpend, OpLelantusJoinSplit, OpLelantusJoinSplitPayload, OpZeroCoinToSigmaRemint, OpSparkSpend:
		return true
	}
	return false
}

// parseFiroTx turns the inputs of private spends (Zerocoin, Sigma, Lelantus,
// Spark), which spend no transparent output, into inputs without a previous
// txid and without coinbase data. The scriptSig is kept.
//
// The indexer skips inputs with an empty txid, so no previous output is ever
// looked up for them. The coinbase field stays empty, so these transactions
// are not reported as coinbase (see IsCoinbaseTx).
func (p *FiroParser) parseFiroTx(tx *bchain.Tx) error {
	for i := range tx.Vin {
		vin := &tx.Vin[i]

		switch {
		case vin.Txid == SpendTxID:
			// null prevout hash: the JSON form of a Zerocoin/Sigma/Lelantus
			// spend, or a Sigma spend (prevout index = coin group) in raw form
			vin.Txid = ""
			vin.Vout = 0
		case vin.Txid == "" && vin.Coinbase != "" && isPrivateSpendScript(vin.Coinbase):
			// a single input with a null prevout, which the bitcoin parser decodes as coinbase
			vin.ScriptSig.Hex = vin.Coinbase
			vin.Coinbase = ""
		case vin.Txid == "" && vin.Coinbase == SpendTxID:
			// stored by earlier versions of parseFiroTx, the scriptSig was kept
			vin.Coinbase = ""
		}
	}

	return nil
}

// vinScript returns the scriptSig of an input, also of a private spend input
// stored before parseFiroTx moved it out of the coinbase field
func vinScript(vin *bchain.Vin) string {
	if vin.Coinbase != "" {
		return vin.Coinbase
	}
	return vin.ScriptSig.Hex
}

// IsCoinbaseTx reports whether tx is a coinbase transaction. It mirrors
// CTransaction::IsCoinBase of firod: a single input with a null prevout whose
// scriptSig does not start with a private spend opcode. In the index a private
// spend looks like a coinbase (one input without address and value).
func (p *FiroParser) IsCoinbaseTx(tx *bchain.Tx) bool {
	if len(tx.Vin) != 1 {
		return false
	}
	vin := &tx.Vin[0]
	if vin.Txid != "" || vin.Coinbase == "" || vin.Coinbase == SpendTxID {
		// earlier versions of parseFiroTx stored SpendTxID as coinbase data of Sigma spends
		return false
	}
	return !isPrivateSpendScript(vin.Coinbase)
}

// isSparkSpend reports whether tx is a Spark spend: one input with a null
// prevout and scriptSig OP_SPARKSPEND (ParseSparkSpend in src/spark/state.cpp)
func isSparkSpend(tx *bchain.Tx) bool {
	if len(tx.Vin) != 1 || tx.Vin[0].Txid != "" {
		return false
	}
	s := vinScript(&tx.Vin[0])
	return len(s) >= 2 && (s[:2] == "d3" || s[:2] == "D3")
}

// PrivateSpendFee returns the fee of a Spark spend. Its inputs are private, so
// the fee cannot be computed as inputs minus outputs; it is the field f of the
// spark::SpendTransaction in the extra payload. The payload is read from tx.Hex
// (raw transaction) or from the sparkData of the verbose JSON in
// tx.CoinSpecificData. readable is false when tx is not a Spark spend; fee is
// nil when the payload is not available or not valid.
//
// Lelantus, Sigma and Zerocoin spends are not handled: their fee is inside the
// proof (lelantus::JoinSplit puts it after the variable-size LelantusProof),
// firod's verbose JSON does not show it, and firod v0.14.18 no longer ships the
// Lelantus library. Their fee stays 0 (the stock Firo Insight API,
// insight-api-zcoin 0.4.5, returns no fee for them either).
func (p *FiroParser) PrivateSpendFee(tx *bchain.Tx) (fee *big.Int, readable bool) {
	if !isSparkSpend(tx) {
		return nil, false
	}
	var txType uint32
	var payload []byte
	if tx.Hex != "" {
		b, err := hex.DecodeString(tx.Hex)
		if err != nil {
			return nil, true
		}
		mtx, err := decodeFiroMsgTx(bytes.NewReader(b))
		if err != nil {
			return nil, true
		}
		txType = uint32(mtx.Version) >> 16
		payload = mtx.Extra
	} else if txJSON, ok := tx.CoinSpecificData.(json.RawMessage); ok {
		var j struct {
			Type      uint32 `json:"type"`
			SparkData string `json:"sparkData"`
		}
		if err := json.Unmarshal(txJSON, &j); err != nil {
			return nil, true
		}
		b, err := hex.DecodeString(j.SparkData)
		if err != nil {
			return nil, true
		}
		txType = j.Type
		payload = b
	} else {
		return nil, true
	}
	f, err := sparkSpendFee(txType, payload)
	if err != nil {
		return nil, true
	}
	return new(big.Int).SetUint64(f), true
}

// sparkSpendFee reads the fee from the serialized spark::SpendTransaction
// (src/libspark/spend_transaction.h of firod v0.14.18.0). Only the fields
// before the fee are parsed.
//
// V1 (TRANSACTION_SPARK): vector<uint64> cover_set_ids,
// map<uint64, uint256> set_id_blockHash, uint64 f.
// V2 (TRANSACTION_SPARK_V2): uint8 version (2), compact size input count,
// compact size output count, input count x uint64 cover_set_ids, compact
// size + map<uint64, uint256> set_id_blockHash, uint64 f.
func sparkSpendFee(txType uint32, payload []byte) (uint64, error) {
	r := bytes.NewReader(payload)
	var inputs uint64
	var err error
	switch txType {
	case TransactionSparkType:
		if inputs, err = wire.ReadVarInt(r, 0); err != nil {
			return 0, err
		}
	case TransactionSparkV2Type:
		version, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		if version != 2 {
			return 0, errors.Errorf("unsupported Spark spend version %d", version)
		}
		if inputs, err = wire.ReadVarInt(r, 0); err != nil {
			return 0, err
		}
		if _, err = wire.ReadVarInt(r, 0); err != nil {
			return 0, err
		}
	default:
		return 0, errors.Errorf("transaction type %d is not a Spark spend", txType)
	}
	if inputs > uint64(r.Len())/8 {
		return 0, errors.New("Spark spend payload too short")
	}
	if _, err = r.Seek(int64(inputs*8), io.SeekCurrent); err != nil {
		return 0, err
	}
	hashes, err := wire.ReadVarInt(r, 0)
	if err != nil {
		return 0, err
	}
	if hashes > uint64(r.Len())/40 {
		return 0, errors.New("Spark spend payload too short")
	}
	if _, err = r.Seek(int64(hashes*40), io.SeekCurrent); err != nil {
		return 0, err
	}
	var f uint64
	if err = binary.Read(r, binary.LittleEndian, &f); err != nil {
		return 0, err
	}
	if f > MaxMoney {
		return 0, errors.Errorf("Spark spend fee %d out of range", f)
	}
	return f, nil
}

func parseBlockHeader(r io.Reader) (*wire.BlockHeader, error) {
	h := &wire.BlockHeader{}
	err := h.Deserialize(r)
	return h, err
}

func isMTP(h *wire.BlockHeader) bool {
	epoch := h.Timestamp.Unix()

	// the genesis block never be MTP block
	return epoch > GenesisBlockTime && epoch >= SwitchToMTPBlockHeader
}

func isProgPow(h *wire.BlockHeader, isTestNet bool) bool {
	epoch := h.Timestamp.Unix()

	// the genesis block never be MTP block
	return isTestNet && epoch >= SwitchToProgPowBlockHeaderTestnet || !isTestNet && epoch >= SwitchToProgPowBlockHeaderMainnet
}

type MTPHashDataRoot struct {
	HashRootMTP [16]uint8
}

type MTPHashData struct {
	BlockMTP [128][128]uint64
}

type MTPBlockHeader struct {
	VersionMTP   int32
	MTPHashValue chainhash.Hash
	Reserved1    chainhash.Hash
	Reserved2    chainhash.Hash
}

type ProgPowBlockHeader struct {
	Nonce64 int64
	MixHash chainhash.Hash
}
