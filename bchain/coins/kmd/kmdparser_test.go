//go:build unittest

package kmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/martinboehm/btcutil"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/btc"
)

// The vectors are real Komodo mainnet transactions with raw hex from
// getrawtransaction on the public Komodo Insight (kmdexplorer.io), each checked
// against its txid by double SHA-256. The JSON is shaped like komodod's
// getrawtransaction verbose output, built from that hex; the addresses come
// from the explorer and are compared with addresses Blockbook derives from the
// output scripts.

// kmdV1 is tx 9b9976b5... (block 1000000), a version 1 transaction paying 5000
// sat to the notary P2PK script (pubkey 020e46e7...) whose address is RXL3YXG2ceaB6C5hfJcN4fvmLH2C34knhA.
const kmdV1Hex = "01000000015674309f527033b560d96132fd5da89329c106593bac7e200322ec136f290e8a1400000048473044022067daaff7786966508bf3ae01812cd566685b8ccc311b44148b603c05cf79952c022055ac346641ec63de8d316555a4a457b9339e7c28d050aee2d0780bb2b09d0bc701ffffffff0188130000000000002321020e46e79a2a8d12b9b5d12c7a91adb4e454edfae43c0a0cb805427d2ac7613fd9ac00000000"

const kmdV1Txid = "9b9976b5360f5765a1f6facb14eecd8e582980ec9a97bb80b6f681185780e6e6"

const kmdV1JSON = `{"hex":"01000000015674309f527033b560d96132fd5da89329c106593bac7e200322ec136f290e8a1400000048473044022067daaff7786966508bf3ae01812cd566685b8ccc311b44148b603c05cf79952c022055ac346641ec63de8d316555a4a457b9339e7c28d050aee2d0780bb2b09d0bc701ffffffff0188130000000000002321020e46e79a2a8d12b9b5d12c7a91adb4e454edfae43c0a0cb805427d2ac7613fd9ac00000000","txid":"9b9976b5360f5765a1f6facb14eecd8e582980ec9a97bb80b6f681185780e6e6","version":1,"locktime":0,"vin":[{"txid":"8a0e296f13ec2203207eac3b5906c12993a85dfd3261d960b53370529f307456","vout":20,"scriptSig":{"hex":"473044022067daaff7786966508bf3ae01812cd566685b8ccc311b44148b603c05cf79952c022055ac346641ec63de8d316555a4a457b9339e7c28d050aee2d0780bb2b09d0bc701"},"sequence":4294967295}],"vout":[{"value":5e-05,"valueSat":5000,"n":0,"scriptPubKey":{"hex":"21020e46e79a2a8d12b9b5d12c7a91adb4e454edfae43c0a0cb805427d2ac7613fd9ac","type":"pubkeyhash","addresses":["RXL3YXG2ceaB6C5hfJcN4fvmLH2C34knhA"]}}],"blockhash":"0cb1d2457eaa58af5028e86e27ac54578fa09558206e7b868ebd35e7005ed8bb","height":1000000,"time":1536365515,"blocktime":1536365515}`

// kmdSapling is tx 39e52750... (block 5168738), a v4 Sapling transaction with two
// P2PKH (R) outputs.
const kmdSaplingHex = "0400008085202f89012ced2df174b214581d9a277c1acb8a38cbe1c67e37c0ae6e5a09a31eb09d2097000000006a47304402205c501cd9f8b6bb4e23c78148bca26b81acd21c1bf8a1e8a461e2dfc9c36b5c3002205c883a8db927d112d09a6472607964c249a02e67cff4095d913e6d764b517da5012102a0f92673eba3a430b2002a23fc166d985f9b0c36ec1fd9ae3ddc17ed86ef0888feffffff0284e51c04000000001976a914d59c0b492a153d764417ad8dd05ae0437a74d2fd88acf4e7d801000000001976a914167ceca62631a19849d1db653b1d3ef8ad1b83f988ac58b0e56a29df4e000000000000000000000000"

const kmdSaplingTxid = "39e52750869181e257312676bc7b6bb159c46f9c6b832b507755a41c6af282ba"

const kmdSaplingJSON = `{"hex":"0400008085202f89012ced2df174b214581d9a277c1acb8a38cbe1c67e37c0ae6e5a09a31eb09d2097000000006a47304402205c501cd9f8b6bb4e23c78148bca26b81acd21c1bf8a1e8a461e2dfc9c36b5c3002205c883a8db927d112d09a6472607964c249a02e67cff4095d913e6d764b517da5012102a0f92673eba3a430b2002a23fc166d985f9b0c36ec1fd9ae3ddc17ed86ef0888feffffff0284e51c04000000001976a914d59c0b492a153d764417ad8dd05ae0437a74d2fd88acf4e7d801000000001976a914167ceca62631a19849d1db653b1d3ef8ad1b83f988ac58b0e56a29df4e000000000000000000000000","txid":"39e52750869181e257312676bc7b6bb159c46f9c6b832b507755a41c6af282ba","overwintered":true,"version":4,"versiongroupid":"892f2085","locktime":1793437784,"vin":[{"txid":"97209db01ea3095a6eaec0377ec6e1cb388acb1a7c279a1d5814b274f12ded2c","vout":0,"scriptSig":{"hex":"47304402205c501cd9f8b6bb4e23c78148bca26b81acd21c1bf8a1e8a461e2dfc9c36b5c3002205c883a8db927d112d09a6472607964c249a02e67cff4095d913e6d764b517da5012102a0f92673eba3a430b2002a23fc166d985f9b0c36ec1fd9ae3ddc17ed86ef0888"},"sequence":4294967294}],"vout":[{"value":0.69002628,"valueSat":69002628,"n":0,"scriptPubKey":{"hex":"76a914d59c0b492a153d764417ad8dd05ae0437a74d2fd88ac","type":"pubkeyhash","addresses":["RUkewWUi45Dk6m6RYqfu9w48SqwPSDN8hx"]}},{"value":0.30992372,"valueSat":30992372,"n":1,"scriptPubKey":{"hex":"76a914167ceca62631a19849d1db653b1d3ef8ad1b83f988ac","type":"pubkeyhash","addresses":["RBL6bdsX3HDJRyrEpLKddVcCTH1hKXjWYH"]}}],"blockhash":"000000309d002d3e32d35dafe28255236cde3e825ba9658cd1184d61f96c8d4b","height":5168738,"time":1793438293,"blocktime":1793438293}`

// kmdP2PKCoinbase is the version 1 coinbase 4f120d43... of block 1000000, which
// pays the block reward to a P2PK script that komodod indexes under RSp8vhyL6hN3yqn5V1qje62pBgBE9fv3Eh.
const kmdP2PKCoinbaseHex = "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff060340420f0102ffffffff0188b6e11100000000232103396ac453b3f23e20f30d4793c5b8ab6ded6993242df4f09fd91eb9a4f8aede84ac9b13935b"

const kmdP2PKCoinbaseTxid = "4f120d437e868afed0bf6323f8c609f4505586dd321e172c06f4e79360c79f16"

const kmdP2PKCoinbaseJSON = `{"hex":"01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff060340420f0102ffffffff0188b6e11100000000232103396ac453b3f23e20f30d4793c5b8ab6ded6993242df4f09fd91eb9a4f8aede84ac9b13935b","txid":"4f120d437e868afed0bf6323f8c609f4505586dd321e172c06f4e79360c79f16","version":1,"locktime":1536365467,"vin":[{"coinbase":"0340420f0102","sequence":4294967295}],"vout":[{"value":3.00005,"valueSat":300005000,"n":0,"scriptPubKey":{"hex":"2103396ac453b3f23e20f30d4793c5b8ab6ded6993242df4f09fd91eb9a4f8aede84ac","type":"pubkeyhash","addresses":["RSp8vhyL6hN3yqn5V1qje62pBgBE9fv3Eh"]}}],"blockhash":"0cb1d2457eaa58af5028e86e27ac54578fa09558206e7b868ebd35e7005ed8bb","height":1000000,"time":1536365515,"blocktime":1536365515}`

// kmdNotarisation is the notarisation tx 3d036ed5... (block 3500000): 13 notary
// inputs, a P2PK output to the notary pubkey (RXL3YXG2ceaB6C5hfJcN4fvmLH2C34knhA)
// and an OP_RETURN carrying the notarised block hash and height.
const kmdNotarisationHex = "0400008085202f890d2f95d75bd7b4dd7548d80a38ea6de9c51acfa9ecb9bdbbcd32367d05e0585e8e170000004948304502210098ca926936f1e81a5eaafb9a5159027050df265c915f948c99735df2f609fa67022074515c9237f07722b56dcf07e32f3ffca2df68a993e789ab9e838cf72dc9a79001ffffffff38b8ed0d56aebdd189c9d9e337a2ade3711d9e37edf3a6b64bbe2a1170d9c3e40d00000049483045022100a80beb6b15bb348f1b59cc993ae72686aad07e0d66380df625885fe5b81bf9a502200b1f3450490bea684056d2cb1b0f49b25f7465cb4bf9754720d9bf1f2fa381c401ffffffff49bf8127537e2196e05529eab3c7f01ce62bd925448e40b5ed323532db79053c0e00000049483045022100f41226926c3ab6c5365c12a0a14248442d66d755169887c510214cc7fc1e75970220695625535bc79178ae9c60dff079f09993de9089a7baf4293b6a190e3d5ab05801ffffffffcebe8b62b04c799a0b5fad6d3267316363a056dc1bdd90590cc397dbe0c314e50000000049483045022100b8e7115ad9f36c6cb89ad83c1db1da2721df9b81953f7fc94f9cc0ff604fbdb902206af95f5107140a9d017d23a761f83b056b800804d4ee95978debdd05cc64cde201ffffffff9bb1ee0eab43ea1b4b3a1e2fc9461c5501820d066cc702d4a94704a19d5041ba020000004847304402204ccca4f3893f566f7c39812cc7cf676485d148a2d67890235f5fb3434190001b02201f3fe3ebdc620fe403bf8fca4f6c40e03a660946961c0c53c1a09adc0014bc2c01ffffffff4f2a0101df9a604ad8a35c34f6043735cb99581228b173e39b9584f91f34d53f0600000049483045022100ca5e665f0fe4a301a4e054593efdd659c2879b0cf5212a17f849dd88ef6c150e022063f049485bdd750366a19f41edf99ddf4c175eff6cc71297711fbea72a70ae8601ffffffff84b6221f3ea06d7167c401250480f1147cdce6202e817264babbc9341b2fefcd06000000494830450221009a6ecd5b5f9d98b6fa883a1f9949af546abe8fa329a4d383fd1b478368986b1802205797093a5ffe463c1bd390bfe3a9e14cfc007286ce8f46409b52f847ff533f8b01ffffffff6b9f86c91c85b2282829c4b0cfe357ff6745f193bab5ed5718f3215a82b6289b00000000494830450221009cad78c9f960bc9777f57218240b18e2978a941a89e3db1180a280720b11270402204dc23369769db34f0a86bb9206238560eb3cce4486de835c778fa8d5a24a95c601ffffffffe85eb50da78e6725b2301066eccd145fa347c5d5cc96e416b7aaf2f278adf8703b0000004948304502210091078d1ec97d54a8f945b0d332d3463bf986db80449f185b0d3d6d1145cdee75022071a96468c001112374c624403590a8a5f21deed1fcd2c366bf76d484b748d3c201ffffffff0cefafd7df771ff14e5895611f45cc4d0f248a37a2afd7b9f9ef95f2a40502810400000049483045022100e46562a00f8c4c90db7700f20f1f0a5e333815401e81b88b65358f498e3ed50102200b66a8a1e771efbae924bebca96f7c439625eedcbbef539e175abacaa211842801ffffffffcfcb12f0f3981b4595d886ea1c6b1f1e402c227a8ac6d3f4d93ae0f5dc788bd62c0000004847304402206755fc543f940584044aa012b9f7eec77589afa944755138cdf4187b099a133c02202045651fcbb559d82276f2c73c147834d1a2b49ba182bc2dc09a2da2eb59851301ffffffff5dce285c48e5ace509dbaa650799aec8da672deec0b63b789e29c59b0d16aae4050000004948304502210088acc38d911a7677fbb6f17f2df28f0c061b77b5a4b7fff6c91cfebca45145a502205844df86b812ae0d75feaec54223f611f189efee5e221c1efd465016c882a45e01ffffffff08526053b94284e075de8e324b9a1dee45ba4186aaf97f37d95cbfb7b5a54eb10900000049483045022100969d383b0069ed6d216257df2bf22df67170d6710865d4e879ff5e68a7aad62f02207ae66fbaf1c14733a90bbd52444ce26f3caa967c141065376ed9f464bda4744101ffffffff02f0810100000000002321020e46e79a2a8d12b9b5d12c7a91adb4e454edfae43c0a0cb805427d2ac7613fd9ac00000000000000002a6a285f6382662c4505109c205907e26c717a49f522e59e6758b2e8e1f14388d94678cdb509004d494c0000000000000000000000000000000000000000"

const kmdNotarisationTxid = "3d036ed527d4bbc563089da13465e9e3753ec385b07c2d972fd2ec75b4b18e11"

const kmdNotarisationJSON = `{"hex":"0400008085202f890d2f95d75bd7b4dd7548d80a38ea6de9c51acfa9ecb9bdbbcd32367d05e0585e8e170000004948304502210098ca926936f1e81a5eaafb9a5159027050df265c915f948c99735df2f609fa67022074515c9237f07722b56dcf07e32f3ffca2df68a993e789ab9e838cf72dc9a79001ffffffff38b8ed0d56aebdd189c9d9e337a2ade3711d9e37edf3a6b64bbe2a1170d9c3e40d00000049483045022100a80beb6b15bb348f1b59cc993ae72686aad07e0d66380df625885fe5b81bf9a502200b1f3450490bea684056d2cb1b0f49b25f7465cb4bf9754720d9bf1f2fa381c401ffffffff49bf8127537e2196e05529eab3c7f01ce62bd925448e40b5ed323532db79053c0e00000049483045022100f41226926c3ab6c5365c12a0a14248442d66d755169887c510214cc7fc1e75970220695625535bc79178ae9c60dff079f09993de9089a7baf4293b6a190e3d5ab05801ffffffffcebe8b62b04c799a0b5fad6d3267316363a056dc1bdd90590cc397dbe0c314e50000000049483045022100b8e7115ad9f36c6cb89ad83c1db1da2721df9b81953f7fc94f9cc0ff604fbdb902206af95f5107140a9d017d23a761f83b056b800804d4ee95978debdd05cc64cde201ffffffff9bb1ee0eab43ea1b4b3a1e2fc9461c5501820d066cc702d4a94704a19d5041ba020000004847304402204ccca4f3893f566f7c39812cc7cf676485d148a2d67890235f5fb3434190001b02201f3fe3ebdc620fe403bf8fca4f6c40e03a660946961c0c53c1a09adc0014bc2c01ffffffff4f2a0101df9a604ad8a35c34f6043735cb99581228b173e39b9584f91f34d53f0600000049483045022100ca5e665f0fe4a301a4e054593efdd659c2879b0cf5212a17f849dd88ef6c150e022063f049485bdd750366a19f41edf99ddf4c175eff6cc71297711fbea72a70ae8601ffffffff84b6221f3ea06d7167c401250480f1147cdce6202e817264babbc9341b2fefcd06000000494830450221009a6ecd5b5f9d98b6fa883a1f9949af546abe8fa329a4d383fd1b478368986b1802205797093a5ffe463c1bd390bfe3a9e14cfc007286ce8f46409b52f847ff533f8b01ffffffff6b9f86c91c85b2282829c4b0cfe357ff6745f193bab5ed5718f3215a82b6289b00000000494830450221009cad78c9f960bc9777f57218240b18e2978a941a89e3db1180a280720b11270402204dc23369769db34f0a86bb9206238560eb3cce4486de835c778fa8d5a24a95c601ffffffffe85eb50da78e6725b2301066eccd145fa347c5d5cc96e416b7aaf2f278adf8703b0000004948304502210091078d1ec97d54a8f945b0d332d3463bf986db80449f185b0d3d6d1145cdee75022071a96468c001112374c624403590a8a5f21deed1fcd2c366bf76d484b748d3c201ffffffff0cefafd7df771ff14e5895611f45cc4d0f248a37a2afd7b9f9ef95f2a40502810400000049483045022100e46562a00f8c4c90db7700f20f1f0a5e333815401e81b88b65358f498e3ed50102200b66a8a1e771efbae924bebca96f7c439625eedcbbef539e175abacaa211842801ffffffffcfcb12f0f3981b4595d886ea1c6b1f1e402c227a8ac6d3f4d93ae0f5dc788bd62c0000004847304402206755fc543f940584044aa012b9f7eec77589afa944755138cdf4187b099a133c02202045651fcbb559d82276f2c73c147834d1a2b49ba182bc2dc09a2da2eb59851301ffffffff5dce285c48e5ace509dbaa650799aec8da672deec0b63b789e29c59b0d16aae4050000004948304502210088acc38d911a7677fbb6f17f2df28f0c061b77b5a4b7fff6c91cfebca45145a502205844df86b812ae0d75feaec54223f611f189efee5e221c1efd465016c882a45e01ffffffff08526053b94284e075de8e324b9a1dee45ba4186aaf97f37d95cbfb7b5a54eb10900000049483045022100969d383b0069ed6d216257df2bf22df67170d6710865d4e879ff5e68a7aad62f02207ae66fbaf1c14733a90bbd52444ce26f3caa967c141065376ed9f464bda4744101ffffffff02f0810100000000002321020e46e79a2a8d12b9b5d12c7a91adb4e454edfae43c0a0cb805427d2ac7613fd9ac00000000000000002a6a285f6382662c4505109c205907e26c717a49f522e59e6758b2e8e1f14388d94678cdb509004d494c0000000000000000000000000000000000000000","txid":"3d036ed527d4bbc563089da13465e9e3753ec385b07c2d972fd2ec75b4b18e11","overwintered":true,"version":4,"versiongroupid":"892f2085","locktime":0,"vin":[{"txid":"8e5e58e0057d3632cdbbbdb9eca9cf1ac5e96dea380ad84875ddb4d75bd7952f","vout":23,"scriptSig":{"hex":"48304502210098ca926936f1e81a5eaafb9a5159027050df265c915f948c99735df2f609fa67022074515c9237f07722b56dcf07e32f3ffca2df68a993e789ab9e838cf72dc9a79001"},"sequence":4294967295},{"txid":"e4c3d970112abe4bb6a6f3ed379e1d71e3ada237e3d9c989d1bdae560dedb838","vout":13,"scriptSig":{"hex":"483045022100a80beb6b15bb348f1b59cc993ae72686aad07e0d66380df625885fe5b81bf9a502200b1f3450490bea684056d2cb1b0f49b25f7465cb4bf9754720d9bf1f2fa381c401"},"sequence":4294967295},{"txid":"3c0579db323532edb5408e4425d92be61cf0c7b3ea2955e096217e532781bf49","vout":14,"scriptSig":{"hex":"483045022100f41226926c3ab6c5365c12a0a14248442d66d755169887c510214cc7fc1e75970220695625535bc79178ae9c60dff079f09993de9089a7baf4293b6a190e3d5ab05801"},"sequence":4294967295},{"txid":"e514c3e0db97c30c5990dd1bdc56a063633167326dad5f0b9a794cb0628bbece","vout":0,"scriptSig":{"hex":"483045022100b8e7115ad9f36c6cb89ad83c1db1da2721df9b81953f7fc94f9cc0ff604fbdb902206af95f5107140a9d017d23a761f83b056b800804d4ee95978debdd05cc64cde201"},"sequence":4294967295},{"txid":"ba41509da10447a9d402c76c060d8201551c46c92f1e3a4b1bea43ab0eeeb19b","vout":2,"scriptSig":{"hex":"47304402204ccca4f3893f566f7c39812cc7cf676485d148a2d67890235f5fb3434190001b02201f3fe3ebdc620fe403bf8fca4f6c40e03a660946961c0c53c1a09adc0014bc2c01"},"sequence":4294967295},{"txid":"3fd5341ff984959be373b128125899cb353704f6345ca3d84a609adf01012a4f","vout":6,"scriptSig":{"hex":"483045022100ca5e665f0fe4a301a4e054593efdd659c2879b0cf5212a17f849dd88ef6c150e022063f049485bdd750366a19f41edf99ddf4c175eff6cc71297711fbea72a70ae8601"},"sequence":4294967295},{"txid":"cdef2f1b34c9bbba6472812e20e6dc7c14f180042501c467716da03e1f22b684","vout":6,"scriptSig":{"hex":"4830450221009a6ecd5b5f9d98b6fa883a1f9949af546abe8fa329a4d383fd1b478368986b1802205797093a5ffe463c1bd390bfe3a9e14cfc007286ce8f46409b52f847ff533f8b01"},"sequence":4294967295},{"txid":"9b28b6825a21f31857edb5ba93f14567ff57e3cfb0c4292828b2851cc9869f6b","vout":0,"scriptSig":{"hex":"4830450221009cad78c9f960bc9777f57218240b18e2978a941a89e3db1180a280720b11270402204dc23369769db34f0a86bb9206238560eb3cce4486de835c778fa8d5a24a95c601"},"sequence":4294967295},{"txid":"70f8ad78f2f2aab716e496ccd5c547a35f14cdec661030b225678ea70db55ee8","vout":59,"scriptSig":{"hex":"48304502210091078d1ec97d54a8f945b0d332d3463bf986db80449f185b0d3d6d1145cdee75022071a96468c001112374c624403590a8a5f21deed1fcd2c366bf76d484b748d3c201"},"sequence":4294967295},{"txid":"810205a4f295eff9b9d7afa2378a240f4dcc451f6195584ef11f77dfd7afef0c","vout":4,"scriptSig":{"hex":"483045022100e46562a00f8c4c90db7700f20f1f0a5e333815401e81b88b65358f498e3ed50102200b66a8a1e771efbae924bebca96f7c439625eedcbbef539e175abacaa211842801"},"sequence":4294967295},{"txid":"d68b78dcf5e03ad9f4d3c68a7a222c401e1f6b1cea86d895451b98f3f012cbcf","vout":44,"scriptSig":{"hex":"47304402206755fc543f940584044aa012b9f7eec77589afa944755138cdf4187b099a133c02202045651fcbb559d82276f2c73c147834d1a2b49ba182bc2dc09a2da2eb59851301"},"sequence":4294967295},{"txid":"e4aa160d9bc5299e783bb6c0ee2d67dac8ae990765aadb09e5ace5485c28ce5d","vout":5,"scriptSig":{"hex":"48304502210088acc38d911a7677fbb6f17f2df28f0c061b77b5a4b7fff6c91cfebca45145a502205844df86b812ae0d75feaec54223f611f189efee5e221c1efd465016c882a45e01"},"sequence":4294967295},{"txid":"b14ea5b5b7bf5cd9377ff9aa8641ba45ee1d9a4b328ede75e08442b953605208","vout":9,"scriptSig":{"hex":"483045022100969d383b0069ed6d216257df2bf22df67170d6710865d4e879ff5e68a7aad62f02207ae66fbaf1c14733a90bbd52444ce26f3caa967c141065376ed9f464bda4744101"},"sequence":4294967295}],"vout":[{"value":0.000988,"valueSat":98800,"n":0,"scriptPubKey":{"hex":"21020e46e79a2a8d12b9b5d12c7a91adb4e454edfae43c0a0cb805427d2ac7613fd9ac","type":"pubkeyhash","addresses":["RXL3YXG2ceaB6C5hfJcN4fvmLH2C34knhA"]}},{"value":0.0,"valueSat":0,"n":1,"scriptPubKey":{"hex":"6a285f6382662c4505109c205907e26c717a49f522e59e6758b2e8e1f14388d94678cdb509004d494c00","type":null}}],"blockhash":"00000000058ded4878267a04562fba5f5455f3f015d0befc30f18a2a5363293c","height":3500000,"time":1689155325,"blocktime":1689155325}`

func kmdParser() *KomodoParser {
	return NewKomodoParser(GetChainParams("main"), &btc.Configuration{})
}

func TestKomodo_NetworkParams(t *testing.T) {
	p := GetChainParams("main")
	if p.Net != 0x8de4eef9 {
		t.Errorf("Net = %#x, want 0x8de4eef9 (pchMessageStart f9 ee e4 8d)", uint32(p.Net))
	}
	if !bytes.Equal(p.PubKeyHashAddrID, []byte{60}) || !bytes.Equal(p.ScriptHashAddrID, []byte{85}) || !bytes.Equal(p.PrivateKeyID, []byte{188}) {
		t.Errorf("address versions = %x/%x wif %x, want 3c/55 wif bc", p.PubKeyHashAddrID, p.ScriptHashAddrID, p.PrivateKeyID)
	}
	if !bytes.Equal(p.HDPublicKeyID[:], []byte{0x04, 0x88, 0xb2, 0x1e}) {
		t.Errorf("xpub version = %x, want 0488b21e", p.HDPublicKeyID)
	}
	if GetChainParams("anything") != p {
		t.Error("every chain name must map to the Komodo mainnet parameters")
	}
}

func TestKomodo_RawTxIsReal(t *testing.T) {
	for name, tt := range map[string]struct{ hex, txid, json, header string }{
		"v1":           {kmdV1Hex, kmdV1Txid, kmdV1JSON, "01000000"},
		"sapling":      {kmdSaplingHex, kmdSaplingTxid, kmdSaplingJSON, "0400008085202f89"},
		"p2pkcoinbase": {kmdP2PKCoinbaseHex, kmdP2PKCoinbaseTxid, kmdP2PKCoinbaseJSON, "01000000"},
		"notarisation": {kmdNotarisationHex, kmdNotarisationTxid, kmdNotarisationJSON, "0400008085202f89"},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := hex.DecodeString(tt.hex)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(tt.hex, tt.header) {
				t.Errorf("header = %x, want %s", raw[:8], tt.header)
			}
			h1 := sha256.Sum256(raw)
			h2 := sha256.Sum256(h1[:])
			for i, j := 0, len(h2)-1; i < j; i, j = i+1, j-1 {
				h2[i], h2[j] = h2[j], h2[i]
			}
			if got := hex.EncodeToString(h2[:]); got != tt.txid {
				t.Errorf("txid = %s, want %s", got, tt.txid)
			}
			tx, err := kmdParser().ParseTxFromJson([]byte(tt.json))
			if err != nil {
				t.Fatal(err)
			}
			if tx.Hex != tt.hex {
				t.Error("JSON hex differs from the fixture hex")
			}
			for i := range tx.Vout {
				if !strings.Contains(tt.hex, tx.Vout[i].ScriptPubKey.Hex) {
					t.Errorf("vout %d script not in the raw transaction", i)
				}
			}
		})
	}
}

func TestKomodo_ParseTxFromJson(t *testing.T) {
	parser := kmdParser()
	tests := []struct {
		name      string
		json      string
		txid      string
		version   int32
		vin       int
		values    []int64
		addresses []string // "" = not searchable (OP_RETURN)
		coinbase  bool
	}{
		{"v1-p2pk", kmdV1JSON, kmdV1Txid, 1, 1, []int64{5000}, []string{"RXL3YXG2ceaB6C5hfJcN4fvmLH2C34knhA"}, false},
		{"v4-sapling", kmdSaplingJSON, kmdSaplingTxid, 4, 1, []int64{69002628, 30992372}, []string{"RUkewWUi45Dk6m6RYqfu9w48SqwPSDN8hx", "RBL6bdsX3HDJRyrEpLKddVcCTH1hKXjWYH"}, false},
		{"v1-p2pk-coinbase", kmdP2PKCoinbaseJSON, kmdP2PKCoinbaseTxid, 1, 1, []int64{300005000}, []string{"RSp8vhyL6hN3yqn5V1qje62pBgBE9fv3Eh"}, true},
		{"v4-notarisation", kmdNotarisationJSON, kmdNotarisationTxid, 4, 13, []int64{98800, 0}, []string{"RXL3YXG2ceaB6C5hfJcN4fvmLH2C34knhA", ""}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := parser.ParseTxFromJson([]byte(tt.json))
			if err != nil {
				t.Fatal(err)
			}
			if tx.Txid != tt.txid || tx.Version != tt.version {
				t.Errorf("txid/version = %s/%d", tx.Txid, tx.Version)
			}
			if len(tx.Vin) != tt.vin || (tx.Vin[0].Coinbase != "") != tt.coinbase {
				t.Errorf("vin = %+v", tx.Vin)
			}
			if len(tx.Vout) != len(tt.values) {
				t.Fatalf("len(vout) = %d, want %d", len(tx.Vout), len(tt.values))
			}
			for i := range tx.Vout {
				if tx.Vout[i].ValueSat.Cmp(big.NewInt(tt.values[i])) != 0 {
					t.Errorf("vout %d value = %s, want %d", i, tx.Vout[i].ValueSat.String(), tt.values[i])
				}
				desc, err := parser.GetAddrDescFromVout(&tx.Vout[i])
				if err != nil {
					t.Fatal(err)
				}
				addrs, searchable, err := parser.GetAddressesFromAddrDesc(desc)
				if err != nil {
					t.Fatal(err)
				}
				if tt.addresses[i] == "" {
					if searchable {
						t.Errorf("vout %d (OP_RETURN) is searchable: %v", i, addrs)
					}
					continue
				}
				if !searchable || !reflect.DeepEqual(addrs, []string{tt.addresses[i]}) {
					t.Errorf("vout %d addresses = %v (searchable %v), want %s", i, addrs, searchable, tt.addresses[i])
				}
			}
		})
	}
}

// TestKomodo_P2PKIndexedAsR checks that a P2PK output is indexed under the
// same descriptor as the P2PKH script of its R address, like komodod's
// address index: GetAddrDescFromVout converts P2PK to P2PKH.
func TestKomodo_P2PKIndexedAsR(t *testing.T) {
	parser := kmdParser()
	for _, tt := range []struct{ pubkey, address string }{
		{"020e46e79a2a8d12b9b5d12c7a91adb4e454edfae43c0a0cb805427d2ac7613fd9", "RXL3YXG2ceaB6C5hfJcN4fvmLH2C34knhA"},
		{"03396ac453b3f23e20f30d4793c5b8ab6ded6993242df4f09fd91eb9a4f8aede84", "RSp8vhyL6hN3yqn5V1qje62pBgBE9fv3Eh"},
	} {
		pub, _ := hex.DecodeString(tt.pubkey)
		vout := bchain.Vout{ScriptPubKey: bchain.ScriptPubKey{Hex: "21" + tt.pubkey + "ac"}}
		fromVout, err := parser.GetAddrDescFromVout(&vout)
		if err != nil {
			t.Fatal(err)
		}
		want := append(append([]byte{0x76, 0xa9, 0x14}, btcutil.Hash160(pub)...), 0x88, 0xac)
		if !bytes.Equal(fromVout, want) {
			t.Errorf("P2PK descriptor = %x, want P2PKH %x", fromVout, want)
		}
		fromAddr, err := parser.GetAddrDescFromAddress(tt.address)
		if err != nil || !bytes.Equal(fromAddr, want) {
			t.Errorf("%s descriptor = %x (%v), want %x", tt.address, fromAddr, err, want)
		}
	}
}

// TestKomodo_AddressRoundTrip checks an R (P2PKH, version 60) address from
// block 5168738 and a b (P2SH, version 85) address, the latter built from the
// script hash 6c344290... with Komodo's P2SH version.
func TestKomodo_AddressRoundTrip(t *testing.T) {
	parser := kmdParser()
	tests := []struct {
		address string
		script  string
	}{
		{"RUkewWUi45Dk6m6RYqfu9w48SqwPSDN8hx", "76a914d59c0b492a153d764417ad8dd05ae0437a74d2fd88ac"},
		{"bNbQH39dNbewzxRKzD813wF4oNJXiJ5Mia", "a9146c34429047366517587a6311df11545b4fa38a5c87"},
	}
	for _, tt := range tests {
		t.Run(tt.address[:1], func(t *testing.T) {
			desc, err := parser.GetAddrDescFromAddress(tt.address)
			if err != nil {
				t.Fatal(err)
			}
			if got := hex.EncodeToString(desc); got != tt.script {
				t.Errorf("script = %s, want %s", got, tt.script)
			}
			script, _ := hex.DecodeString(tt.script)
			addrs, searchable, err := parser.GetAddressesFromAddrDesc(script)
			if err != nil || !searchable || !reflect.DeepEqual(addrs, []string{tt.address}) {
				t.Errorf("addresses = %v (searchable %v, err %v)", addrs, searchable, err)
			}
		})
	}
	// Zcash t1 addresses must not decode with Komodo parameters.
	if _, err := parser.GetAddrDescFromAddress("t1Ps5rSpdGcvzkbf3hcBXq8apTdFZGjXRy7"); err == nil {
		t.Error("a Zcash t1 address decoded with Komodo parameters")
	}
}

func TestKomodo_PackUnpackTx(t *testing.T) {
	parser := kmdParser()
	tx, err := parser.ParseTxFromJson([]byte(kmdNotarisationJSON))
	if err != nil {
		t.Fatal(err)
	}
	tx.Blocktime = tx.Time
	packed, err := parser.PackTx(tx, 3500000, tx.Time)
	if err != nil {
		t.Fatal(err)
	}
	got, height, err := parser.UnpackTx(packed)
	if err != nil {
		t.Fatal(err)
	}
	if height != 3500000 || got.Txid != kmdNotarisationTxid || got.Hex != kmdNotarisationHex || len(got.Vin) != 13 || len(got.Vout) != 2 {
		t.Errorf("unpacked tx = %+v (height %d)", got, height)
	}
	if got.Vout[1].ScriptPubKey.Hex != tx.Vout[1].ScriptPubKey.Hex || got.Vin[12].Txid != tx.Vin[12].Txid {
		t.Errorf("unpacked tx differs: %+v", got)
	}
}
