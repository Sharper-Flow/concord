package store

import "testing"

// migrationChecksumPins freezes the SHA-256 checksum of every migration's
// SQL as this file defines it. A migration's recorded checksum is the
// identity every store that applied it verifies on open, so an edit to a
// shipped migration's text strands every database that recorded the old
// bytes: v8.4.0 re-indented two closing backticks inside raw string
// literals and no released binary since could upgrade a pre-v8.4.0 store.
// The pins make any text change a deliberate act: an edit fails here until
// the pin is updated, and a pin for a shipped migration may only change by
// restoring the canonical text and recording the variant in
// migrationShippedVariantChecksums with the release range that carried it.
// A new migration fails here until its pin is added alongside it.
var migrationChecksumPins = map[int]string{
	1:  "1c29bcb9d35642dd6847375186d47173c21b0ec6c80138820dc79ec9197b241c",
	2:  "934914d5db84a453a71d4b33f2d01100c4d43377e551d602a0f1ba87620982b0",
	3:  "74b6c236ec8c8944ef954d6f4d9ac9d0b0b320ed8c8064f4920fb68024932238",
	4:  "3a46e5e477d6bb190867d906aa71556e2ae58a2b448160a9b49032bdf64b9585",
	5:  "63cf35a22e41c552e16b51d78c73fa7ed25cd695ea84f94fbd8d557dd072437a",
	6:  "78d78805b6c78831dbff4174ab259c1e7899961087cde9b2576cfd085f9d8710",
	7:  "02778971721fcb0ae2dc8066029ead05e7a05103937af77c59dcaba3681c3a39",
	8:  "c146e7d5f96affa537a270b9906d3e291d8607d1cda5337fbe20145162aa7c84",
	9:  "9f5801ace57d5e2cc875f21eba0aba56f52b547798a4e747496cd1c9810d5a7f",
	10: "58b13bc699b565a9349f396722f11d8afa84f40c69dfb68b53c3314cf260543b",
	11: "7baa6c1e82f65d1a9c73d6d5a1ebcf12ef08a35a04ba650dcbeca5dcbcb30d05",
	12: "9d5f35998ddaa6793e98955e12916c6fe178ce997018c71214deff604626500f",
	13: "c2aa8ce672c62ac628ac07d07f44bdc527a7980a5039bee8c7493f8d2cfc1245",
	14: "0a131f93ea53e17d9ec5959b3720ed9f60daaffe4767649fec6711241086bc7e",
	15: "4d5a8140aff3c10f0b4362c2ea287b1dffe6ef9d6a251bd79710987b6336c748",
	16: "91c7e6debe110f458113b6875bae8f4c01ca634dd2781521e18b25ab846c5b65",
	17: "cef827ad0b11a5f1d25d43805893efa1542fdbd03fc9f8ced1664f11975f22e4",
	18: "86eb77c156fe03b8cb22a5c48783fb2879b655390c26f98ff115543eb1b186ca",
	19: "0c5a2596b56ab7179baacd978cb8317619c1004b40920411f3f54125c97fafe4",
	20: "ac04dcf566236960cc42b7e4d941f81655200ca0a1d533fdad6c342b4d62416d",
	21: "1313f708389783b82d605996abb1ef64f510650fdc8ee3739aed26d99e6dd864",
	22: "5002b1fe0ae9c71938e2e5ea167d11819b87a75f1cbbdbd6cb61438c03181c01",
	23: "5c110bf8aea53049b0ec7989f4d0b2053fc2c28d5f2f9e291e8579da044e1d75",
	24: "7e3b52927b62c347c6048cdf7fe37370132431f9a677b80600ebd40dc24856da",
	25: "f797d1c280e6a47d39d2e96a2011826c586698aad6f37d40d23be1f37ec0b59f",
	26: "972e557afbe05d6c2cdc86a8f2db50b92424883f86952800288be3712258d174",
	27: "c3349bfbc71c659b853d115bd5a87cf2b1373d81594208f27cd428cab42daf7a",
	28: "b3eadd3768eeeb1b6b46594c7e773397948f89ca557819fe0f793948839fe99d",
	29: "1eeea0527786ce15fbaa19b71f5fe411426c6ef1b7f803712d5eae0c0de094a0",
	30: "7ef362cdb2142ab62bd4209cbe566a28e18210778130c8f24a41da186fbc3231",
	31: "f4b15965598c61d1bb8e2f5780729b85fb015b06962e9c9da9d2acdbf1f30aff",
	32: "47459b5c634f90092e6dafa2b98d93ce5113ca68bacdbd1b41a6b50315556eb6",
	33: "51a3e4ab0a6bcd542c8a062fef2e5a1f8ee1ea53ee5c8fa8fa5ef6a4325c033f",
	34: "fa1d1f9d9ee0eee4b250d3c79dcf8bd451016689b87d3d60e9965174d3b21894",
	35: "0a295ae169ca9f232749a49d6109170165974449447eb74294abf2919917c6b4",
	36: "79e4fe6bb4d5c6f99787d7c58f1b317d6bba6bf5b3f312193e23bccda93de293",
	37: "bfd16642442afc179ca64ad847077eef6f9a8709512c1e9190a15ba55c39c810",
	38: "cd28aab5dcf69460ae6076c52bbf397fb8f26cfba4cecd0fcd865588cd85161f",
	39: "ece84b5df8eadb82f2f9e370763d822808d8c99e6174a75a6bdae11571f2ed88",
	40: "86caab1f627a7850d19c3748e19f70dfcf41aaa15e4d8e6885c7d0463aab20fa",
	41: "72865bf70a6d2a75ad7f8308e24bfba71fb0da440f82c364a618e0af90c964c9",
	42: "57112e161416bf97f9568c550ee313cf0ea12cb6926c1d6f5bab25485818e155",
	43: "6662ca1ef134469c380914b4ab3186ec5d8de731251439a96f2ddb5c134f20da",
	44: "3b34e8ef7420cee2d3f38722ac63ec26d4216b4a86b9b4e99796ec7e94b1b79f",
	45: "c45b86984791a1add0595411be48d54bba488a842db4e41e4461ee3de885f318",
	46: "43a1f11ec606c823047bef2789813d32f88861398c11707cf5328f4eda8b3259",
	47: "74a2e596927fceccde085157a74bebf94103f28818ab9c55ba05485f4836d885",
	48: "64d4b7c91af41c0c0a2c2ab303d4b727cf9ee1df5921bd1e610a6b3df63f5462",
	49: "82be0495e62706821edc7413d76f3d604817a1f5fbe47e77687a7d2525a6af9c",
	50: "254da438ccb5bd2a927030e3f2baf40ac39a05226c463b79444bcaeb2c53fb9f",
	51: "4c19d5ca85aed8e9dced392e6e8942d33816df9f73b6229bb36a704e9b55b28f",
	52: "37c47eb30dc9242f671d5f0d447be649654b1557c862f5278205e5ea0782cb09",
	53: "9d5635eb5828877b89476dab8db6e6c9a1f9cd417fbff82a2154321bbf64c326",
	54: "4773715fd9a8757374000ca8534ceb1d110399f49134673a960703ca0bb6484b",
	55: "9e821e08fede41322e65d7f6cd3e81c2e85cdc8993f250f64cf4a115f3ac9247",
	56: "742e587332fe8f10a4a31ac70d247b60e3603862e9a3509f7f07cac67049605f",
	57: "ac88291382f02dd946cde97c75e455124b91a2a0fc3773f024231b7e16867d8d",
	58: "ecfc4b59eadf07db45659fd92bc4fcfd1a88894d97ba727e3bc1cc2418c0cc19",
	59: "e8ca97186c4d9a141594fbbddf977fdfff9175b1347893809be085223757e71d",
	60: "e495b9948d7ed5af7cb340f0153adb38823988352ed52d89a4f10f169fbd4ae6",
	61: "a4df900a4f6fb40ee38c8c7de8d5fd52f9007a2940fb7b5049fcdb7da8168d68",
	62: "00c45358a2846e5d74a82c54a7108d9ffb29725d46114933c5d984e4c3861e29",
	63: "c6bc90d6c9b6775097bb797f9d17d3203e6b28a9a124f6acfecd722498015627",
	64: "a49cdd844929a98c3bf5e31578af637eb7e72db92acc398794423ef0fe34aad7",
	65: "353dcb102bc560b7e3e13b4cf5566d39780c47a4fc58e3f67c54ea5fe8480a09",
	66: "f797feae8a1949bc1c82581081beceefcef4ac65ca9c9c0be40d300c4f8b115b",
	67: "1f7c4298538427eac5aed38e848889b21ec4441bca77d12fd3749d8387c63e79",
	68: "9936de1ddc970b8322466b318c7aacf01e8467d1084315f7f875a1237248baa6",
	69: "d565ca92ea989b163b18ff9d8071b7067fce2b7d262dbf477e4d80210fe95fe4",
	70: "fea5b8ede2bc920a30ffcd1ee9a59b6a03d107373bfb2724cd25ff732a9952ea",
	71: "7d6f67fd0a04478be41e94cf3f0a5e8ea50c4ac48ac5f1b5f8ffddace0b585d3",
	72: "274972c12c58f3e381ab650e42a5cab693fa8dd145b976c82c78cc0b5be0bd2a",
	73: "ee41eff6d89fd13ab889ee19d7d5893699c4dfbf712994cb42567a28f123c6c5",
	74: "624c72ddff8db51c15a3f139ce4a7daa229802fe358a134538d29f50388fd683",
	75: "72c8d3b056ff852f0ea5eb1971808f16d2a72d9b5ab8da5445bdc636aff68328",
	76: "84673ba8ad751a5e0d80b2c0101aadf139bfb6028abfc45f3c54c7db1bd29e04",
	77: "57e0d66468bc2ec0aed6eed89e81b891e5d4d8a682f8d18dc09165ce90f0eb8f",
	78: "ffaf1335d53d51fb8dbc8f5ef111d86ac62848e1dc0b58d44acad8458d5f9d63",
}

func TestMigrationChecksumPinsMatchDefinitions(t *testing.T) {
	t.Parallel()
	defined := make(map[int]migration, len(migrations))
	for _, m := range migrations {
		defined[m.Version] = m
	}
	if len(migrationChecksumPins) != len(defined) {
		for version := range defined {
			if _, ok := migrationChecksumPins[version]; !ok {
				t.Errorf("migration %d has no checksum pin; add one beside its definition", version)
			}
		}
		for version := range migrationChecksumPins {
			if _, ok := defined[version]; !ok {
				t.Errorf("pin %d names no defined migration; remove the stale pin", version)
			}
		}
		t.Fatalf("pin set covers %d of %d migrations", len(migrationChecksumPins), len(defined))
	}
	for version, pin := range migrationChecksumPins {
		if got := defined[version].checksum(); got != pin {
			t.Errorf("migration %d checksum = %s, pin = %s; restore the recorded text or follow the variant-table discipline for a shipped edit", version, got, pin)
		}
	}
}
