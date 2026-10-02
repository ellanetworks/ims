package milenage

import (
	"bytes"
	"crypto/aes"
	"encoding/hex"
	"errors"
	"testing"
)

type testSet struct {
	n                                  int
	k, rand, sqn, amf, op, opc         string
	f1, f1Star, f2, f5, f3, f4, f5Star string
}

// TS 35.207 §4 to §6.
var implementorsSets = []testSet{
	{1, "465b5ce8b199b49faa5f0a2ee238a6bc", "23553cbe9637a89d218ae64dae47bf35", "ff9bb4d0b607", "b9b9", "cdc202d5123e20f62b6d676ac72cb318", "cd63cb71954a9f4e48a5994e37a02baf", "4a9ffac354dfafb3", "01cfaf9ec4e871e9", "a54211d5e3ba50bf", "aa689c648370", "b40ba9a3c58b2a05bbf0d987b21bf8cb", "f769bcd751044604127672711c6d3441", "451e8beca43b"},
	{2, "0396eb317b6d1c36f19c1c84cd6ffd16", "c00d603103dcee52c4478119494202e8", "fd8eef40df7d", "af17", "ff53bade17df5d4e793073ce9d7579fa", "53c15671c60a4b731c55b4a441c0bde2", "5df5b31807e258b0", "a8c016e51ef4a343", "d3a628ed988620f0", "c47783995f72", "58c433ff7a7082acd424220f2b67c556", "21a8c1f929702adb3e738488b9f5c5da", "30f1197061c1"},
	{3, "fec86ba6eb707ed08905757b1bb44b8f", "9f7c8d021accf4db213ccff0c7f71a6a", "9d0277595ffc", "725c", "dbc59adcb6f9a0ef735477b7fadf8374", "1006020f0a478bf6b699f15c062e42b3", "9cabc3e99baf7281", "95814ba2b3044324", "8011c48c0c214ed2", "33484dc2136b", "5dbdbb2954e8f3cde665b046179a5098", "59a92d3b476a0443487055cf88b2307b", "deacdd848cc6"},
	{4, "9e5944aea94b81165c82fbf9f32db751", "ce83dbc54ac0274a157c17f80d017bd6", "0b604a81eca8", "9e09", "223014c5806694c007ca1eeef57f004f", "a64a507ae1a2a98bb88eb4210135dc87", "74a58220cba84c49", "ac2cc74a96871837", "f365cd683cd92e96", "f0b9c08ad02e", "e203edb3971574f5a94b0d61b816345d", "0c4524adeac041c4dd830d20854fc46b", "6085a86c6f63"},
	{5, "4ab1deb05ca6ceb051fc98e77d026a84", "74b0cd6031a1c8339b2b6ce2b8c4a186", "e880a1b580b6", "9f07", "2d16c5cd1fdf6b22383584e3bef2a8d8", "dcf07cbd51855290b92a07a9891e523e", "49e785dd12626ef2", "9e85790336bb3fa2", "5860fc1bce351e7e", "31e11a609118", "7657766b373d1c2138f307e3de9242f9", "1c42e960d89b8fa99f2744e0708ccb53", "fe2555e54aa9"},
	{6, "6c38a116ac280c454f59332ee35c8c4f", "ee6466bc96202c5a557abbeff8babf63", "414b98222181", "4464", "1ba00a1a7c6700ac8c3ff3e96ad08725", "3803ef5363b947c6aaa225e58fae3934", "078adfb488241a57", "80246b8d0186bcf1", "16c8233f05a0ac28", "45b0f69ab06c", "3f8c7587fe8e4b233af676aede30ba3b", "a7466cc1e6b2a1337d49d3b66e95d7b4", "1f53cd2b1113"},
}

// TS 35.208 §4.3.
var conformanceSets = []testSet{
	{1, "465b5ce8b199b49faa5f0a2ee238a6bc", "23553cbe9637a89d218ae64dae47bf35", "ff9bb4d0b607", "b9b9", "cdc202d5123e20f62b6d676ac72cb318", "cd63cb71954a9f4e48a5994e37a02baf", "4a9ffac354dfafb3", "01cfaf9ec4e871e9", "a54211d5e3ba50bf", "aa689c648370", "b40ba9a3c58b2a05bbf0d987b21bf8cb", "f769bcd751044604127672711c6d3441", "451e8beca43b"},
	{2, "465b5ce8b199b49faa5f0a2ee238a6bc", "23553cbe9637a89d218ae64dae47bf35", "ff9bb4d0b607", "b9b9", "cdc202d5123e20f62b6d676ac72cb318", "cd63cb71954a9f4e48a5994e37a02baf", "4a9ffac354dfafb3", "01cfaf9ec4e871e9", "a54211d5e3ba50bf", "aa689c648370", "b40ba9a3c58b2a05bbf0d987b21bf8cb", "f769bcd751044604127672711c6d3441", "451e8beca43b"},
	{3, "fec86ba6eb707ed08905757b1bb44b8f", "9f7c8d021accf4db213ccff0c7f71a6a", "9d0277595ffc", "725c", "dbc59adcb6f9a0ef735477b7fadf8374", "1006020f0a478bf6b699f15c062e42b3", "9cabc3e99baf7281", "95814ba2b3044324", "8011c48c0c214ed2", "33484dc2136b", "5dbdbb2954e8f3cde665b046179a5098", "59a92d3b476a0443487055cf88b2307b", "deacdd848cc6"},
	{4, "9e5944aea94b81165c82fbf9f32db751", "ce83dbc54ac0274a157c17f80d017bd6", "0b604a81eca8", "9e09", "223014c5806694c007ca1eeef57f004f", "a64a507ae1a2a98bb88eb4210135dc87", "74a58220cba84c49", "ac2cc74a96871837", "f365cd683cd92e96", "f0b9c08ad02e", "e203edb3971574f5a94b0d61b816345d", "0c4524adeac041c4dd830d20854fc46b", "6085a86c6f63"},
	{5, "4ab1deb05ca6ceb051fc98e77d026a84", "74b0cd6031a1c8339b2b6ce2b8c4a186", "e880a1b580b6", "9f07", "2d16c5cd1fdf6b22383584e3bef2a8d8", "dcf07cbd51855290b92a07a9891e523e", "49e785dd12626ef2", "9e85790336bb3fa2", "5860fc1bce351e7e", "31e11a609118", "7657766b373d1c2138f307e3de9242f9", "1c42e960d89b8fa99f2744e0708ccb53", "fe2555e54aa9"},
	{6, "6c38a116ac280c454f59332ee35c8c4f", "ee6466bc96202c5a557abbeff8babf63", "414b98222181", "4464", "1ba00a1a7c6700ac8c3ff3e96ad08725", "3803ef5363b947c6aaa225e58fae3934", "078adfb488241a57", "80246b8d0186bcf1", "16c8233f05a0ac28", "45b0f69ab06c", "3f8c7587fe8e4b233af676aede30ba3b", "a7466cc1e6b2a1337d49d3b66e95d7b4", "1f53cd2b1113"},
	{7, "2d609d4db0ac5bf0d2c0de267014de0d", "194aa756013896b74b4a2a3b0af4539e", "6bf69438c2e4", "5f67", "460a48385427aa39264aac8efc9e73e8", "c35a0ab0bcbfc9252caff15f24efbde0", "bd07d3003b9e5cc3", "bcb6c2fcad152250", "8c25a16cd918a1df", "7e6455f34cf3", "4cd0846020f8fa0731dd47cbdc6be411", "88ab80a415f15c73711254a1d388f696", "dc6dd01e8f15"},
	{8, "a530a7fe428fad1082c45eddfce13884", "3a4c2b3245c50eb5c71d08639395764d", "f63f5d768784", "b90e", "511c6c4e83e38c89b1c5d8dde62426fa", "27953e49bc8af6dcc6e730eb80286be3", "53761fbd679b0bad", "21adfd334a10e7ce", "a63241e1ffc3e5ab", "88196c47986f", "10f05bab75a99a5fbb98a9c287679c3b", "f9ec0865eb32f22369cade40c59c3a44", "c987a3d23115"},
	{9, "d9151cf04896e25830bf2e08267b8360", "f761e5e93d603feb730e27556cb8a2ca", "47ee0199820a", "9113", "75fc2233a44294ee8e6de25c4353d26b", "c4c93effe8a08138c203d4c27ce4e3d9", "66cc4be44862af1f", "7a4b8d7a8753f246", "4a90b2171ac83a76", "82a0f5287a71", "71236b7129f9b22ab77ea7a54c96da22", "90527ebaa5588968db41727325a04d9e", "527dbf41f35f"},
	{10, "a0e2971b6822e8d354a18cc235624ecb", "08eff828b13fdb562722c65c7f30a9b2", "db5c066481e0", "716b", "323792faca21fb4d5d6f13c145a9d2c1", "82a26f22bba9e9488f949a10d98e9cc4", "9485fe24621cb9f6", "bce325ce03e2e9b9", "4bc2212d8624910a", "a2f858aa9e5d", "08cef6d004ec61471a3c3cda048137fa", "ed0318ca5deb9206272f6e8fa64ba411", "74e76fbbec38"},
	{11, "0da6f7ba86d5eac8a19cf563ac58642d", "679ac4dbacd7d233ff9d6806f4149ce3", "6e2331d692ad", "224a", "4b9a26fa459e3acbff36f4015de3bdc1", "0db1071f8767562ca43a0a64c41e8d08", "2831d7ae9088e492", "9b2e16951135d523", "6fc30fee6d123523", "4c539a26e1fa", "69b1cae7c7429d975e245cacb05a517c", "74f24e8c26df58e1b38d7dcd4f1b7fbd", "07861e126928"},
	{12, "77b45843c88e58c10d202684515ed430", "4c47eb3076dc55fe5106cb2034b8cd78", "fe1a8731005d", "ad25", "bf3286c7a51409ce95724d503bfe6e70", "d483afae562409a326b5bb0b20c4d762", "08332d7e9f484570", "ed41b734489d5207", "aefa357beac2a87a", "30ff25cdadf6", "908c43f0569cb8f74bc971e706c36c5f", "c251df0d888dd9329bcf46655b226e40", "e84ed0d4677e"},
	{13, "729b17729270dd87ccdf1bfe29b4e9bb", "311c4c929744d675b720f3b7e9b1cbd0", "c85c4cf65916", "5bb2", "d04c9c35bd2262fa810d2924d036fd13", "228c2f2f06ac3268a9e616ee16db4ba1", "ff794fe2f827ebf8", "24fe4dc61e874b52", "98dbbd099b3b408d", "5380d158cfe3", "44c0f23c5493cfd241e48f197e1d1012", "0c9fb81613884c2535dd0eabf3b440d8", "87ac3b559fb6"},
	{14, "d32dd23e89dc662354ca12eb79dd32fa", "cf7d0ab1d94306950bf12018fbd46887", "484107e56a43", "b5e6", "fe75905b9da47d356236d0314e09c32e", "d22a4b4180a5325708a5ff70d9f67ec7", "cf19d62b6a809866", "5d269537e45e2ce6", "af4a411e1139f2c2", "217af49272ad", "5af86b80edb70df5292cc1121cbad50c", "7f4d6ae7440e18789a8b75ad3f42f03a", "900e101c677e"},
	{15, "af7c65e1927221de591187a2c5987a53", "1f0f8578464fd59b64bed2d09436b57a", "3d627b01418d", "84f6", "0c7acb8d95b7d4a31c5aca6d26345a88", "a4cf5c8155c08a7eff418e5443b98e55", "c37cae7805642032", "68cd09a452d8db7c", "7bffa5c2f41fbc05", "837fd7b74419", "3f8c3f3ccf7625bf77fc94bcfd22fd26", "abcbae8fd46115e9961a55d0da5f2078", "56e97a6090b1"},
	{16, "5bd7ecd3d3127a41d12539bed4e7cf71", "59b75f14251c75031d0bcbac1c2c04c7", "a298ae8929dc", "d056", "f967f76038b920a9cd25e10c08b49924", "76089d3c0ff3efdc6e36721d4fceb747", "c3f25cd94309107e", "b0c8ba343665afcc", "7e3f44c7591f6f45", "5be11495525d", "d42b2d615e49a03ac275a5aef97af892", "0b3f8d024fe6bfafaa982b8f82e319c2", "4d6a34a1e4eb"},
	{17, "6cd1c6ceb1e01e14f1b82316a90b7f3d", "f69b78f300a0568bce9f0cb93c4be4c9", "b4fce5feb059", "e4bb", "078bfca9564659ecd8851e84e6c59b48", "a219dc37f1dc7d66738b5843c799f206", "69a90869c268cb7b", "2e0fdcf9fd1cfa6a", "70f6bdb9ad21525f", "1c408a858b3e", "6edaf99e5bd9f85d5f36d91c1272fb4b", "d61c853c280dd9c46f297baec386de17", "aa4ae52daa30"},
	{18, "b73a90cbcf3afb622dba83c58a8415df", "b120f1c1a0102a2f507dd543de68281f", "f1e8a523a36d", "471b", "b672047e003bb952dca6cb8af0e5b779", "df0c67868fa25f748b7044c6e7c245b8", "ebd70341bcd415b0", "12359f5d82220c14", "479dd25c20792d63", "aefdaa5ddd99", "66195dbed0313274c5ca7766615fa25e", "66bec707eb2afc476d7408a8f2927b36", "12ec2b87fbb1"},
	{19, "5122250214c33e723a5dd523fc145fc0", "81e92b6c0ee0e12ebceba8d92a99dfa5", "16f3b3f70fc2", "c3ab", "c9e8763286b5b9ffbdf56e1297d0887b", "981d464c7c52eb6e5036234984ad0bcf", "2a5c23d15ee351d5", "62dae3853f3af9d2", "28d7b0f2a2ec3de5", "ada15aeb7bb8", "5349fbe098649f948f5d2e973a81c00f", "9744871ad32bf9bbd1dd5ce54e3e2e5a", "d461bc15475d"},
	{20, "90dca4eda45b53cf0f12d7c9c3bc6a89", "9fddc72092c6ad036b6e464789315b78", "20f813bd4141", "61df", "3ffcfe5b7b1111589920d3528e84e655", "cb9cccc4b9258e6dca4760379fb82581", "09db94eab4f8149e", "a29468aa9775b527", "a95100e2760952cd", "83cfd54db913", "b5f2da03883b69f96bf52e029ed9ac45", "b4721368bc16ea67875c5598688bb0ef", "4f2039392ddc"},
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()

	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// TS 35.207 §3: the kernel function is AES-128.
func TestRijndael(t *testing.T) {
	for _, tc := range []struct{ k, plain, cipher string }{
		{"465b5ce8b199b49faa5f0a2ee238a6bc", "ee36f7cf037d37d3692f7f0399e7949a", "9e2980c59739da67b136355e3cede6a2"},
		{"0396eb317b6d1c36f19c1c84cd6ffd16", "93cc3640c5d6a521d81235bd0882bf0a", "009a9e0996561525f611667bbf79e226"},
		{"fec86ba6eb707ed08905757b1bb44b8f", "8f7a8f0d108b7f2d97a53eacc1d958d9", "5d9bce854decaf0da93d28b7e35f608c"},
		{"9e5944aea94b81165c82fbf9f32db751", "68c98bbfab628ec1adf2a3d90c34a751", "db2944cce8e683cd03fff19931a12135"},
		{"4ab1deb05ca6ceb051fc98e77d026a84", "a840b1dd60249aa322016b4b31daf3b8", "02bffada7137c492c00e8452d8c76eaa"},
		{"6c38a116ac280c454f59332ee35c8c4f", "d66789eff5996b9cffd89e0a77148657", "bdf226fecf9ff9961e5c2621b764efb1"},
	} {
		b, err := aes.NewCipher(unhex(t, tc.k))
		if err != nil {
			t.Fatal(err)
		}

		got := make([]byte, 16)
		b.Encrypt(got, unhex(t, tc.plain))

		if want := unhex(t, tc.cipher); !bytes.Equal(got, want) {
			t.Errorf("E_K(%s) = %x, want %x", tc.plain, got, want)
		}
	}
}

func TestImplementorsTestData(t *testing.T) {
	testFunctions(t, implementorsSets)
}

func TestConformanceTestData(t *testing.T) {
	testFunctions(t, conformanceSets)
}

func testFunctions(t *testing.T, sets []testSet) {
	for _, ts := range sets {
		k, rand, sqn, amf := unhex(t, ts.k), unhex(t, ts.rand), unhex(t, ts.sqn), unhex(t, ts.amf)

		opc, err := OPc(k, unhex(t, ts.op))
		if err != nil {
			t.Fatal(err)
		}

		c, err := New(k, opc)
		if err != nil {
			t.Fatal(err)
		}

		f1, _ := c.F1(rand, sqn, amf)
		f1Star, _ := c.F1Star(rand, sqn, amf)
		res, ck, ik, ak, _ := c.F2345(rand)
		akStar, _ := c.F5Star(rand)

		for _, out := range []struct {
			name      string
			got       []byte
			wantValue string
		}{
			{"OPc", opc, ts.opc},
			{"f1", f1, ts.f1},
			{"f1*", f1Star, ts.f1Star},
			{"f2", res, ts.f2},
			{"f3", ck, ts.f3},
			{"f4", ik, ts.f4},
			{"f5", ak, ts.f5},
			{"f5*", akStar, ts.f5Star},
		} {
			if want := unhex(t, out.wantValue); !bytes.Equal(out.got, want) {
				t.Errorf("set %d: %s = %x, want %x", ts.n, out.name, out.got, want)
			}
		}
	}
}

func TestVectorAndRespond(t *testing.T) {
	for _, ts := range conformanceSets {
		k, opc, rand, amf := unhex(t, ts.k), unhex(t, ts.opc), unhex(t, ts.rand), unhex(t, ts.amf)
		sqn := sqnValue(unhex(t, ts.sqn))

		v, err := GenerateVector(k, opc, rand, sqn, amf)
		if err != nil {
			t.Fatal(err)
		}

		autn := xor(unhex(t, ts.sqn), unhex(t, ts.f5))
		autn = append(append(autn, amf...), unhex(t, ts.f1)...)

		if !bytes.Equal(v.AUTN, autn) || !bytes.Equal(v.XRES, unhex(t, ts.f2)) ||
			!bytes.Equal(v.CK, unhex(t, ts.f3)) || !bytes.Equal(v.IK, unhex(t, ts.f4)) || !bytes.Equal(v.RAND, rand) {
			t.Fatalf("set %d: vector = %x", ts.n, v)
		}

		r, err := Respond(k, opc, rand, v.AUTN)
		if err != nil {
			t.Fatalf("set %d: Respond: %v", ts.n, err)
		}

		if r.SQN != sqn || !bytes.Equal(r.AMF, amf) || !bytes.Equal(r.RES, v.XRES) ||
			!bytes.Equal(r.CK, v.CK) || !bytes.Equal(r.IK, v.IK) {
			t.Fatalf("set %d: response = %+v, want SQN %x and the vector's values", ts.n, r, sqn)
		}

		for i := range v.AUTN {
			bad := bytes.Clone(v.AUTN)
			bad[i] ^= 0x80

			if _, err := Respond(k, opc, rand, bad); !errors.Is(err, ErrMACFailure) {
				t.Fatalf("set %d: Respond with AUTN octet %d flipped = %v, want a MAC failure", ts.n, i, err)
			}
		}

		wrongK := bytes.Clone(k)
		wrongK[0] ^= 1

		if _, err := Respond(wrongK, opc, rand, v.AUTN); !errors.Is(err, ErrMACFailure) {
			t.Fatalf("set %d: Respond with the wrong K = %v, want a MAC failure", ts.n, err)
		}
	}
}

func TestAUTS(t *testing.T) {
	for _, ts := range conformanceSets {
		k, opc, rand := unhex(t, ts.k), unhex(t, ts.opc), unhex(t, ts.rand)
		sqnMS := sqnValue(unhex(t, ts.sqn))

		auts, err := AUTS(k, opc, rand, sqnMS)
		if err != nil {
			t.Fatal(err)
		}

		c, _ := New(k, opc)
		macS, _ := c.F1Star(rand, unhex(t, ts.sqn), []byte{0, 0})
		want := append(xor(unhex(t, ts.sqn), unhex(t, ts.f5Star)), macS...)

		if !bytes.Equal(auts, want) {
			t.Fatalf("set %d: AUTS = %x, want %x", ts.n, auts, want)
		}

		got, err := Resync(k, opc, rand, auts)
		if err != nil || got != sqnMS {
			t.Fatalf("set %d: Resync = %x, %v, want %x", ts.n, got, err, sqnMS)
		}

		auts[len(auts)-1] ^= 1

		if _, err := Resync(k, opc, rand, auts); !errors.Is(err, ErrMACFailure) {
			t.Fatalf("set %d: Resync with a bad MAC-S = %v, want a MAC failure", ts.n, err)
		}
	}
}

func TestLengths(t *testing.T) {
	k, opc := make([]byte, 16), make([]byte, 16)

	for name, err := range map[string]error{
		"short K":    func() error { _, err := New(k[:15], opc); return err }(),
		"short OP":   func() error { _, err := OPc(k, opc[:15]); return err }(),
		"short RAND": func() error { _, err := GenerateVector(k, opc, make([]byte, 15), 1, []byte{0, 0}); return err }(),
		"long AMF":   func() error { _, err := GenerateVector(k, opc, make([]byte, 16), 1, []byte{0, 0, 0}); return err }(),
		"SQN":        func() error { _, err := GenerateVector(k, opc, make([]byte, 16), MaxSQN+1, []byte{0, 0}); return err }(),
		"AUTN":       func() error { _, err := Respond(k, opc, make([]byte, 16), make([]byte, 15)); return err }(),
		"AUTS":       func() error { _, err := Resync(k, opc, make([]byte, 16), make([]byte, 13)); return err }(),
		"SQN_MS":     func() error { _, err := AUTS(k, opc, make([]byte, 16), MaxSQN+1); return err }(),
	} {
		if !errors.Is(err, ErrLength) {
			t.Errorf("%s: %v, want ErrLength", name, err)
		}
	}
}

func xor(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}

	return out
}
