package main

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// Token auto-detection from installed Discord clients via Windows DPAPI.

var (
	crypt32                = syscall.NewLazyDLL("crypt32.dll")
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procCryptUnprotectData = crypt32.NewProc("CryptUnprotectData")
	procLocalFree          = kernel32.NewProc("LocalFree")
)

type dataBlob struct {
	cbData uint32
	pbData *byte
}

var discordDirs = map[string]string{
	"Discord":        "discord",
	"Discord Canary": "discordcanary",
	"Discord PTB":    "discordptb",
}

var (
	encryptedTokenRe = regexp.MustCompile(`dQw4w9WgXcQ:([A-Za-z0-9+/=]+)`)
	plainTokenRe     = regexp.MustCompile(`[\w-]{24,26}\.[\w-]{6}\.[\w-]{25,110}`)
)

func dpapiDecrypt(data []byte) ([]byte, error) {
	inBlob := dataBlob{
		cbData: uint32(len(data)),
		pbData: &data[0],
	}
	var outBlob dataBlob

	r, _, err := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&inBlob)),
		0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&outBlob)),
	)
	if r == 0 {
		return nil, fmt.Errorf("CryptUnprotectData failed: %v", err)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(outBlob.pbData)))

	result := make([]byte, outBlob.cbData)
	copy(result, unsafe.Slice(outBlob.pbData, outBlob.cbData))
	return result, nil
}

func findDiscordClients() map[string]string {
	appdata := os.Getenv("APPDATA")
	if appdata == "" {
		return nil
	}
	found := make(map[string]string)
	for label, dirname := range discordDirs {
		p := filepath.Join(appdata, dirname)
		if _, err := os.Stat(filepath.Join(p, "Local State")); err == nil {
			found[label] = p
		}
	}
	return found
}

func getMasterKey(discordDir string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(discordDir, "Local State"))
	if err != nil {
		return nil, err
	}
	var state struct {
		OSCrypt struct {
			EncryptedKey string `json:"encrypted_key"`
		} `json:"os_crypt"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(state.OSCrypt.EncryptedKey)
	if err != nil {
		return nil, err
	}
	if len(raw) < 5 || string(raw[:5]) != "DPAPI" {
		return nil, fmt.Errorf("not a DPAPI key")
	}
	return dpapiDecrypt(raw[5:])
}

func decryptToken(encrypted, key []byte) (string, error) {
	if len(encrypted) < 15+16 { // 3 version + 12 nonce + 16 tag minimum
		return "", fmt.Errorf("encrypted data too short")
	}
	nonce := encrypted[3:15]
	ciphertext := encrypted[15:]

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func scanLevelDB(dir string) (encrypted [][]byte, plain [][]byte) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".ldb") && !strings.HasSuffix(name, ".log") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		for _, match := range encryptedTokenRe.FindAllSubmatch(data, -1) {
			decoded, err := base64.StdEncoding.DecodeString(string(match[1]))
			if err == nil {
				encrypted = append(encrypted, decoded)
			}
		}
		for _, match := range plainTokenRe.FindAll(data, -1) {
			plain = append(plain, match)
		}
	}
	return
}

func extractTokens(discordDir string) []string {
	leveldbDir := filepath.Join(discordDir, "Local Storage", "leveldb")
	encBlobs, plainBlobs := scanLevelDB(leveldbDir)

	var tokens []string
	seen := make(map[string]bool)

	key, err := getMasterKey(discordDir)
	if err == nil && key != nil {
		for _, blob := range encBlobs {
			tok, err := decryptToken(blob, key)
			if err == nil && tok != "" && !seen[tok] {
				seen[tok] = true
				tokens = append(tokens, tok)
			}
		}
	}

	for _, raw := range plainBlobs {
		tok := string(raw)
		if !seen[tok] {
			seen[tok] = true
			tokens = append(tokens, tok)
		}
	}
	return tokens
}

// DetectAccounts finds Discord accounts from installed clients.
func DetectAccounts(onChecking func(label string)) []struct {
	Label string
	Token string
	User  *User
} {
	clients := findDiscordClients()
	var found []struct {
		Label string
		Token string
		User  *User
	}

	for label, path := range clients {
		if onChecking != nil {
			onChecking(label)
		}
		tokens := extractTokens(path)
		for _, tok := range tokens {
			c := NewClient(tok)
			user, err := c.GetMe()
			if err == nil && user != nil {
				found = append(found, struct {
					Label string
					Token string
					User  *User
				}{label, tok, user})
				break
			}
		}
	}
	return found
}

// SaveToken saves a token to config file.
func SaveToken(token string) error {
	dir := filepath.Join(os.Getenv("USERPROFILE"), ".config", "discord-purger")
	os.MkdirAll(dir, 0700)
	return os.WriteFile(filepath.Join(dir, "token"), []byte(token), 0600)
}

// LoadToken loads a saved token.
func LoadToken() string {
	path := filepath.Join(os.Getenv("USERPROFILE"), ".config", "discord-purger", "token")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ResolveToken tries to get a valid token: explicit > saved > auto-detect.
func ResolveToken(explicit string) (string, *User) {
	// Explicit token
	if explicit != "" && explicit != "auto" {
		c := NewClient(explicit)
		if u, err := c.GetMe(); err == nil {
			fmt.Printf("  %s token valid for %s\n", colorGreen("OK"), colorCyan(u.Username))
			return explicit, u
		}
		fmt.Printf("  %s provided token is invalid\n", colorRed("ERR"))
		return "", nil
	}

	// Saved token (skip if "auto" forces re-detect)
	if explicit != "auto" {
		if saved := LoadToken(); saved != "" {
			c := NewClient(saved)
			if u, err := c.GetMe(); err == nil {
				fmt.Printf("  %s using saved token — %s\n", colorGreen("OK"), colorCyan(u.Username))
				return saved, u
			}
			fmt.Printf("  %s saved token expired, re-detecting...\n", colorYellow("!"))
		}
	}

	// Auto-detect from Discord clients
	fmt.Printf("  %s scanning for Discord clients...\n", colorDim("..."))
	accounts := DetectAccounts(func(label string) {
		fmt.Printf("    %s checking %s...\n", colorDim("~"), label)
	})

	if len(accounts) == 0 {
		fmt.Printf("  %s no Discord accounts found\n", colorRed("ERR"))
		fmt.Println("  use --token <your_token> to provide one manually")
		return "", nil
	}

	if len(accounts) == 1 {
		a := accounts[0]
		fmt.Printf("  %s found %s via %s\n", colorGreen("OK"), colorCyan(a.User.Username), colorBold(a.Label))
		SaveToken(a.Token)
		return a.Token, a.User
	}

	// Multiple accounts
	fmt.Printf("\n  found %d accounts:\n\n", len(accounts))
	for i, a := range accounts {
		fmt.Printf("    %s  %s  %s\n", colorBold(fmt.Sprintf("%d", i+1)), colorCyan(a.User.Username), colorDim(a.Label))
	}
	fmt.Println()
	fmt.Printf("  pick account [1-%d]: ", len(accounts))

	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	choice, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || choice < 1 || choice > len(accounts) {
		return "", nil
	}
	a := accounts[choice-1]
	SaveToken(a.Token)
	return a.Token, a.User
}
