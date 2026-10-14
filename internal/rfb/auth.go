package rfb

import (
	"crypto/rand"
)

// 本文件对应 UltraVNC rfb/vncauth.c 与 winvnc/winvnc/vncauth.c 的密码管理与认证逻辑。

// vncFixedKey 对应 vncauth.c 中的 fixedkey，用于本地密码文件的“混淆”存储。
var vncFixedKey = [8]byte{23, 82, 107, 6, 35, 78, 88, 7}

const (
	maxPwLen     = 8
	challengeSize = 16
)

// padPassword 对应 vncauth.c 中“密码补零到 8 字节”的逻辑。
func padPassword(passwd string) [8]byte {
	var key [8]byte
	for i := 0; i < 8; i++ {
		if i < len(passwd) {
			key[i] = passwd[i]
		} else {
			key[i] = 0
		}
	}
	return key
}

// vncEncryptBytes 对应 vncauth.c 的 vncEncryptBytes()：
// 使用密码作为 DES 密钥，逐 8 字节加密 challenge 数据。
func vncEncryptBytes(data []byte, passwd string) {
	key := padPassword(passwd)
	d := new(d3des)
	d.deskey(key[:], en0)
	for i := 0; i < len(data); i += 8 {
		copy(data[i:i+8], d.des(data[i:i+8]))
	}
}

// vncRandomBytes 对应 vncauth.c 的 vncRandomBytes()，使用加密安全随机源。
func vncRandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// EncryptPasswd 对应 vncEncryptPasswd()：使用 fixedkey DES 加密密码 (用于反混淆/存储)。
func EncryptPasswd(passwd string) [8]byte {
	enc := padPassword(passwd)
	d := new(d3des)
	d.deskey(vncFixedKey[:], en0)
	copy(enc[:], d.des(enc[:]))
	return enc
}

// DecryptPasswd 对应 vncDecryptPasswd()：使用 fixedkey DES 解密密码。
func DecryptPasswd(encrypted [8]byte) string {
	d := new(d3des)
	d.deskey(vncFixedKey[:], de1)
	plain := d.des(encrypted[:])
	// 截断到首个 NUL
	for i := 0; i < len(plain); i++ {
		if plain[i] == 0 {
			plain = plain[:i]
			break
		}
	}
	return string(plain)
}

// verifyChallenge 校验客户端返回的 16 字节响应是否与用 password 加密的 challenge 一致。
func verifyChallenge(challenge, response []byte, password string) bool {
	if len(challenge) != challengeSize || len(response) != challengeSize {
		return false
	}
	expected := make([]byte, challengeSize)
	copy(expected, challenge)
	vncEncryptBytes(expected, password)
	return subtleBytesEqual(expected, response)
}

func subtleBytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}