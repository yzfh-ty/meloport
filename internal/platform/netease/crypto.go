package netease

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5" // The upstream eapi protocol specifies MD5, not a password hash.
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/url"
	"strings"
)

const (
	linuxKey       = "rFgB&h#%2?^eDg:Q"
	weapiKey       = "0CoJUm6Qyw8W8jud"
	eapiKey        = "e82ckenh8dichen8"
	weapiIV        = "0102030405060708"
	weapiPublicKey = "-----BEGIN PUBLIC KEY-----\nMIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDgtQn2JZ34ZC28NWYpAUd98iZ37BUrX/aKzmFbt7clFSs6sXqHauqKWqdtLkF2KexO40H1YTX8z2lSgBBOAxLsvaklV8k4cBFK9snQXE9/DDaFt6Rr7iVZMldczhC0JNgTz+SHXT6CBHuX3e9SdB1Ua44oncaTWz7OBGLbCiK45wIDAQAB\n-----END PUBLIC KEY-----"
)

func padded(plain []byte) []byte {
	n := aes.BlockSize - len(plain)%aes.BlockSize
	out := make([]byte, len(plain)+n)
	copy(out, plain)
	for i := len(plain); i < len(out); i++ {
		out[i] = byte(n)
	}
	return out
}

func ecbEncrypt(plain []byte, key string) ([]byte, error) {
	block, err := aes.NewCipher([]byte(key))
	if err != nil {
		return nil, errors.New("invalid internal NetEase protocol key")
	}
	plain = padded(plain)
	out := make([]byte, len(plain))
	for i := 0; i < len(plain); i += block.BlockSize() {
		block.Encrypt(out[i:i+block.BlockSize()], plain[i:i+block.BlockSize()])
	}
	return out, nil
}

func cbcEncrypt(plain []byte, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("invalid internal NetEase protocol key")
	}
	plain = padded(plain)
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, []byte(weapiIV)).CryptBlocks(out, plain)
	return out, nil
}

func linuxForm(target string, params any) ([]byte, error) {
	plain, err := json.Marshal(map[string]any{"method": "POST", "url": "https://music.163.com" + target, "params": params})
	if err != nil {
		return nil, errors.New("cannot encode NetEase request")
	}
	encrypted, err := ecbEncrypt(plain, linuxKey)
	if err != nil {
		return nil, err
	}
	return []byte(url.Values{"eparams": {strings.ToUpper(hex.EncodeToString(encrypted))}}.Encode()), nil
}

func weapiForm(params any) ([]byte, error) {
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		return nil, errors.New("cannot generate NetEase request nonce")
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	for i, b := range secret {
		secret[i] = alphabet[int(b)%len(alphabet)]
	}
	return weapiFormWithSecret(params, secret)
}

func weapiFormWithSecret(params any, secret []byte) ([]byte, error) {
	if len(secret) != 16 {
		return nil, errors.New("invalid NetEase request nonce length")
	}
	secret = append([]byte(nil), secret...)
	plain, err := json.Marshal(params)
	if err != nil {
		return nil, errors.New("cannot encode NetEase request")
	}
	first, err := cbcEncrypt(plain, []byte(weapiKey))
	if err != nil {
		return nil, err
	}
	second, err := cbcEncrypt([]byte(base64.StdEncoding.EncodeToString(first)), secret)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode([]byte(weapiPublicKey))
	if block == nil {
		return nil, errors.New("invalid internal NetEase public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, errors.New("invalid internal NetEase public key")
	}
	public, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("invalid internal NetEase public key")
	}
	for i, j := 0, len(secret)-1; i < j; i, j = i+1, j-1 {
		secret[i], secret[j] = secret[j], secret[i]
	}
	message := new(big.Int).SetBytes(secret)
	encoded := new(big.Int).Exp(message, big.NewInt(int64(public.E)), public.N)
	rsaBytes := encoded.FillBytes(make([]byte, public.Size()))
	return []byte(url.Values{"params": {base64.StdEncoding.EncodeToString(second)}, "encSecKey": {hex.EncodeToString(rsaBytes)}}.Encode()), nil
}

func eapiForm(target string, params any) ([]byte, error) {
	plain, err := json.Marshal(params)
	if err != nil {
		return nil, errors.New("cannot encode NetEase search request")
	}
	digest := md5.Sum([]byte("nobody" + target + "use" + string(plain) + "md5forencrypt"))
	message := target + "-36cd479b6b5-" + string(plain) + "-36cd479b6b5-" + hex.EncodeToString(digest[:])
	encrypted, err := ecbEncrypt([]byte(message), eapiKey)
	if err != nil {
		return nil, err
	}
	return []byte(url.Values{"params": {strings.ToUpper(hex.EncodeToString(encrypted))}}.Encode()), nil
}
