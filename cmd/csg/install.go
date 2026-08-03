package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	configBlockBegin = "# >>> Codex Session Guard (managed; do not edit) >>>"
	configBlockEnd   = "# <<< Codex Session Guard <<<"
)

type guardHookSpec struct {
	Event      string
	EventID    string
	Matcher    string
	HasMatcher bool
	Timeout    float64
}

var guardHookSpecs = []guardHookSpec{
	{Event: "SessionStart", EventID: "session_start", Matcher: hookMatcher, HasMatcher: true, Timeout: 10},
	{Event: "SessionEnd", EventID: "session_end", Timeout: 3},
}

type hookTrust struct {
	Key  string
	Hash string
}

func installMain(args []string) int {
	flags := flag.NewFlagSet("install", flag.ContinueOnError)
	binDir := flags.String("bin-dir", "", "installation directory")
	if flags.Parse(args) != nil {
		return 2
	}
	if *binDir == "" {
		fmt.Fprintln(os.Stderr, "install requires --bin-dir")
		return 2
	}
	binAbs, err := filepath.Abs(*binDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.MkdirAll(binAbs, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := validateInstallTargets(binAbs); err != nil {
		fmt.Fprintln(os.Stderr, "Installation directory validation failed:", err)
		return 1
	}
	legacyOwned, err := ownedLegacyCommands(binAbs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Legacy command ownership validation failed:", err)
		return 1
	}

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	binaryHash, err := fileSHA256(self)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to hash the installation binary:", err)
		return 1
	}

	codexStateRoot, err := codexHome()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	hooksPath := filepath.Join(codexStateRoot, "hooks.json")
	configPath := filepath.Join(codexStateRoot, "config.toml")
	settingsFile, err := settingsPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	hooksCreated := false
	if _, statErr := os.Stat(hooksPath); errors.Is(statErr, os.ErrNotExist) {
		hooksCreated = true
	} else if statErr != nil {
		fmt.Fprintln(os.Stderr, "Failed to inspect hooks.json:", statErr)
		return 1
	}
	previous, previousErr := loadSettings()
	previousMatches := previousErr == nil && pathsEqual(previous.InstallDir, binAbs)
	if previousMatches {
		hooksCreated = previous.HooksFileWasCreated
	}
	settings := Settings{
		Version:             2,
		InstallDir:          binAbs,
		CodexHome:           codexStateRoot,
		BinaryHash:          binaryHash,
		HooksFileWasCreated: hooksCreated,
		InstalledAt:         time.Now().UTC(),
	}
	if previousMatches {
		// These fields are retained only so pre-0.3 run records can still resume.
		settings.RealCodexPath = previous.RealCodexPath
		settings.CodexVersion = previous.CodexVersion
		settings.ManagedPackageRoot = previous.ManagedPackageRoot
		settings.ManagedBy = previous.ManagedBy
	}
	paths := []string{
		hooksPath,
		configPath,
		settingsFile,
	}
	for _, name := range installedCommandNames() {
		paths = append(paths, filepath.Join(binAbs, name))
	}
	paths = append(paths, legacyOwned...)
	snapshots, err := captureFiles(paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to create the installation rollback snapshot:", err)
		return 1
	}
	installErr := func() error {
		for _, path := range legacyOwned {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove legacy Codex Session Guard command %s: %w", filepath.Base(path), err)
			}
		}
		for _, name := range []string{commandName("codex-session-guard"), commandName("csg")} {
			if err := copyExecutable(self, filepath.Join(binAbs, name)); err != nil {
				return fmt.Errorf("install %s: %w", name, err)
			}
		}
		if err := installHook(&settings); err != nil {
			return fmt.Errorf("install session hooks: %w", err)
		}
		if err := writeJSONAtomic(settingsFile, settings); err != nil {
			return fmt.Errorf("save settings: %w", err)
		}
		if err := verifyCommittedInstall(settings); err != nil {
			return fmt.Errorf("post-install validation: %w", err)
		}
		return nil
	}()
	if installErr != nil {
		if rollbackErr := restoreFiles(snapshots); rollbackErr != nil {
			fmt.Fprintln(os.Stderr, "Installation failed:", installErr)
			fmt.Fprintln(os.Stderr, "Rollback also failed; inspect the backups manually:", rollbackErr)
			return 1
		}
		fmt.Fprintln(os.Stderr, "Installation failed and the previous state was restored:", installErr)
		return 1
	}
	fmt.Println("Codex Session Guard installed in", binAbs)
	return 0
}

type fileSnapshot struct {
	Path   string
	Exists bool
	Data   []byte
	Mode   os.FileMode
}

func captureFiles(paths []string) ([]fileSnapshot, error) {
	snapshots := make([]fileSnapshot, 0, len(paths))
	for _, path := range paths {
		snapshot := fileSnapshot{Path: path}
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			snapshots = append(snapshots, snapshot)
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			return nil, fmt.Errorf("expected a file but found a directory: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		snapshot.Exists = true
		snapshot.Data = data
		snapshot.Mode = info.Mode()
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func restoreFiles(snapshots []fileSnapshot) error {
	var result error
	for index := len(snapshots) - 1; index >= 0; index-- {
		snapshot := snapshots[index]
		if !snapshot.Exists {
			if err := os.Remove(snapshot.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, err)
			}
			continue
		}
		if current, err := os.ReadFile(snapshot.Path); err == nil && bytes.Equal(current, snapshot.Data) {
			continue
		}
		if err := writeBytesAtomic(snapshot.Path, snapshot.Data, snapshot.Mode); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func uninstallMain(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "uninstall does not accept arguments")
		return 2
	}
	settings, err := loadSettings()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := uninstallHook(settings); err != nil {
		fmt.Fprintln(os.Stderr, "Failed to remove session hooks:", err)
		return 1
	}
	if runtime.GOOS != "windows" {
		paths := make([]string, 0, len(installedCommandNames())+len(legacyCommandBases()))
		for _, name := range installedCommandNames() {
			paths = append(paths, filepath.Join(settings.InstallDir, name))
		}
		legacyOwned, legacyErr := ownedLegacyCommands(settings.InstallDir)
		if legacyErr != nil {
			fmt.Fprintln(os.Stderr, "Failed to verify legacy command ownership:", legacyErr)
			return 1
		}
		paths = append(paths, legacyOwned...)
		for _, path := range paths {
			if hash, hashErr := fileSHA256(path); errors.Is(hashErr, os.ErrNotExist) {
				continue
			} else if hashErr != nil || (settings.BinaryHash != "" && !strings.EqualFold(hash, settings.BinaryHash)) {
				fmt.Fprintln(os.Stderr, "Skipping command file with unverified ownership:", path)
				continue
			}
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				fmt.Fprintln(os.Stderr, "Failed to remove command file:", err)
				return 1
			}
		}
	}
	fmt.Println("Session hooks removed; the uninstall script can now remove command files.")
	return 0
}

func validateInstallTargets(binDir string) error {
	names := installedCommandNames()
	existing := make([]string, 0, len(names))
	for _, name := range names {
		path := filepath.Join(binDir, name)
		if _, err := os.Stat(path); err == nil {
			existing = append(existing, path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if len(existing) == 0 {
		return nil
	}
	previous, err := loadSettings()
	if err != nil || !pathsEqual(previous.InstallDir, binDir) {
		return errors.New("the target directory contains matching command names whose ownership cannot be verified")
	}
	manager := filepath.Join(binDir, commandName("codex-session-guard"))
	reference, err := fileSHA256(manager)
	if err != nil {
		return err
	}
	if previous.BinaryHash == "" || !strings.EqualFold(reference, previous.BinaryHash) {
		return fmt.Errorf("existing %s does not match the version recorded in settings.json", commandName("codex-session-guard"))
	}
	output, err := exec.Command(manager, "version").CombinedOutput()
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(output)), "codex-session-guard ") {
		return fmt.Errorf("cannot verify ownership of existing %s", commandName("codex-session-guard"))
	}
	for _, path := range existing {
		hash, err := fileSHA256(path)
		if err != nil {
			return err
		}
		if !strings.EqualFold(hash, previous.BinaryHash) {
			return fmt.Errorf("refusing to overwrite a same-named file from a different installation: %s", path)
		}
	}
	return nil
}

func legacyCommandBases() []string {
	return []string{"codex", "codex-recover"}
}

func ownedLegacyCommands(binDir string) ([]string, error) {
	previous, err := loadSettings()
	if err != nil || !pathsEqual(previous.InstallDir, binDir) || previous.BinaryHash == "" {
		return nil, nil
	}
	owned := make([]string, 0, len(legacyCommandBases()))
	for _, base := range legacyCommandBases() {
		path := filepath.Join(binDir, commandName(base))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		hash, err := fileSHA256(path)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(hash, previous.BinaryHash) {
			owned = append(owned, path)
		}
	}
	return owned, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func copyExecutable(source, target string) error {
	sourceAbs, _ := filepath.Abs(source)
	targetAbs, _ := filepath.Abs(target)
	if strings.EqualFold(sourceAbs, targetAbs) {
		return nil
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(target), ".guard-install-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return err
	}
	return os.Rename(tmpPath, target)
}

func commandName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func installedCommandNames() []string {
	return []string{
		commandName("csg"),
		commandName("codex-session-guard"),
	}
}

func verifyCommittedInstall(settings Settings) error {
	for _, name := range installedCommandNames() {
		path := filepath.Join(settings.InstallDir, name)
		hash, err := fileSHA256(path)
		if err != nil {
			return fmt.Errorf("verify %s: %w", name, err)
		}
		if settings.BinaryHash != "" && !strings.EqualFold(hash, settings.BinaryHash) {
			return fmt.Errorf("%s does not match the installed binary hash", name)
		}
	}
	return checkHookInstallation(settings)
}

func pathsEqual(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func writeBytesAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".guard-write-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func installHook(settings *Settings) error {
	if err := os.MkdirAll(settings.CodexHome, 0o700); err != nil {
		return err
	}
	hooksPath := filepath.Join(settings.CodexHome, "hooks.json")
	_, statErr := os.Stat(hooksPath)
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		return statErr
	}

	root := map[string]any{}
	if !created {
		data, err := os.ReadFile(hooksPath)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("existing hooks.json is not valid JSON: %w", err)
		}
		if err := backupFileOnce(hooksPath, "hooks.json.before-session-guard"); err != nil {
			return fmt.Errorf("back up hooks.json: %w", err)
		}
	} else {
		root["description"] = "User hooks (includes Codex Session Guard)"
	}
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		hooks = map[string]any{}
		root["hooks"] = hooks
	}
	indices := make(map[string]int, len(guardHookSpecs))
	for _, spec := range guardHookSpecs {
		indices[spec.Event] = installGuardHookGroup(hooks, spec)
	}
	if err := writeJSONAtomic(hooksPath, root); err != nil {
		return err
	}

	hooksAbs, err := filepath.Abs(hooksPath)
	if err != nil {
		return err
	}
	trusts := make([]hookTrust, 0, len(guardHookSpecs))
	for _, spec := range guardHookSpecs {
		trust := hookTrust{
			Key:  fmt.Sprintf("%s:%s:%d:0", filepath.Clean(hooksAbs), spec.EventID, indices[spec.Event]),
			Hash: guardHookHash(spec),
		}
		trusts = append(trusts, trust)
		if spec.Event == "SessionStart" {
			settings.HookKey = trust.Key
			settings.HookHash = trust.Hash
		} else {
			settings.SessionEndHookKey = trust.Key
			settings.SessionEndHookHash = trust.Hash
		}
	}
	return installTrustBlock(filepath.Join(settings.CodexHome, "config.toml"), trusts)
}

func installGuardHookGroup(hooks map[string]any, spec guardHookSpec) int {
	groups, _ := hooks[spec.Event].([]any)
	guardGroup := expectedGuardGroup(spec)
	groupIndex := -1
	for index, value := range groups {
		group, ok := value.(map[string]any)
		if !ok || !groupHasGuardCommand(group) {
			continue
		}
		handlers, _ := group["hooks"].([]any)
		kept := handlersWithoutGuard(handlers)
		if len(kept) == 0 && groupIndex < 0 {
			groups[index] = guardGroup
			groupIndex = index
		} else {
			// Keep the group position so unrelated handlers retain trust keys.
			group["hooks"] = kept
		}
	}
	if groupIndex < 0 {
		groups = append(groups, guardGroup)
		groupIndex = len(groups) - 1
	}
	hooks[spec.Event] = groups
	return groupIndex
}

func expectedGuardGroup(spec guardHookSpec) map[string]any {
	group := map[string]any{
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": hookCommand,
			"timeout": spec.Timeout,
			"async":   false,
		}},
	}
	if spec.HasMatcher {
		group["matcher"] = spec.Matcher
	}
	return group
}

func groupHasGuardCommand(value any) bool {
	group, ok := value.(map[string]any)
	if !ok {
		return false
	}
	handlers, _ := group["hooks"].([]any)
	for _, value := range handlers {
		handler, ok := value.(map[string]any)
		if ok && isGuardHookCommand(handler["command"]) {
			return true
		}
	}
	return false
}

func groupIsExpectedGuard(value any, spec guardHookSpec) bool {
	group, ok := value.(map[string]any)
	expectedFields := 1
	if spec.HasMatcher {
		expectedFields++
	}
	if !ok || len(group) != expectedFields {
		return false
	}
	if spec.HasMatcher && group["matcher"] != spec.Matcher {
		return false
	}
	handlers, ok := group["hooks"].([]any)
	if !ok || len(handlers) != 1 {
		return false
	}
	handler, ok := handlers[0].(map[string]any)
	if !ok || len(handler) != 4 {
		return false
	}
	timeout, timeoutOK := handler["timeout"].(float64)
	async, asyncOK := handler["async"].(bool)
	return handler["type"] == "command" && handler["command"] == hookCommand &&
		timeoutOK && timeout == spec.Timeout && asyncOK && !async
}

func handlersWithoutGuard(handlers []any) []any {
	kept := make([]any, 0, len(handlers))
	for _, value := range handlers {
		handler, ok := value.(map[string]any)
		if ok && isGuardHookCommand(handler["command"]) {
			continue
		}
		kept = append(kept, value)
	}
	return kept
}

func isGuardHookCommand(value any) bool {
	command, ok := value.(string)
	return ok && (command == hookCommand || command == legacyHookCommand)
}

func guardHookHash(spec guardHookSpec) string {
	identity := map[string]any{
		"event_name": spec.EventID,
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": hookCommand,
			"timeout": spec.Timeout,
			"async":   false,
		}},
	}
	if spec.HasMatcher {
		identity["matcher"] = spec.Matcher
	}
	data, _ := json.Marshal(identity)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func installTrustBlock(configPath string, trusts []hookTrust) error {
	contents := ""
	if data, err := os.ReadFile(configPath); err == nil {
		contents = string(data)
		if err := backupFileOnce(configPath, "config.toml.before-session-guard"); err != nil {
			return fmt.Errorf("back up config.toml: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	clean, err := stripManagedBlock(contents)
	if err != nil {
		return err
	}
	clean = strings.TrimRight(clean, "\r\n")
	var block strings.Builder
	block.WriteString(configBlockBegin)
	block.WriteByte('\n')
	for _, trust := range trusts {
		fmt.Fprintf(&block, "[hooks.state.%s]\ntrusted_hash = %s\n", strconv.Quote(trust.Key), strconv.Quote(trust.Hash))
	}
	block.WriteString(configBlockEnd)
	block.WriteByte('\n')
	if clean != "" {
		clean += "\n\n"
	}
	return writeTextAtomic(configPath, clean+block.String())
}

func stripManagedBlock(contents string) (string, error) {
	start := strings.Index(contents, configBlockBegin)
	if start < 0 {
		return contents, nil
	}
	endRel := strings.Index(contents[start:], configBlockEnd)
	if endRel < 0 {
		return "", errors.New("the managed Session Guard block in config.toml is incomplete")
	}
	end := start + endRel + len(configBlockEnd)
	for end < len(contents) && (contents[end] == '\r' || contents[end] == '\n') {
		end++
	}
	return contents[:start] + contents[end:], nil
}

func writeTextAtomic(path, contents string) error {
	return writeBytesAtomic(path, []byte(contents), 0o600)
}

func backupFileOnce(source, name string) error {
	guardStateRoot, err := guardHome()
	if err != nil {
		return err
	}
	target := filepath.Join(guardStateRoot, "backup", name)
	if _, err := os.Stat(target); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return writeBytesAtomic(target, data, 0o600)
}

func uninstallHook(settings Settings) error {
	hooksPath := filepath.Join(settings.CodexHome, "hooks.json")
	if data, err := os.ReadFile(hooksPath); err == nil {
		var root map[string]any
		if err := json.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("existing hooks.json is not valid JSON: %w", err)
		}
		if hooks, ok := root["hooks"].(map[string]any); ok {
			for _, spec := range guardHookSpecs {
				removeGuardHookGroup(hooks, spec.Event)
			}
			if len(hooks) == 0 {
				delete(root, "hooks")
			}
		}
		if settings.HooksFileWasCreated && hooksFileIsDisposable(root) {
			if err := os.Remove(hooksPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else {
			if err := writeJSONAtomic(hooksPath, root); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	configPath := filepath.Join(settings.CodexHome, "config.toml")
	data, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	clean, err := stripManagedBlock(string(data))
	if err != nil {
		return err
	}
	return writeTextAtomic(configPath, clean)
}

func removeGuardHookGroup(hooks map[string]any, event string) {
	groups, _ := hooks[event].([]any)
	cleaned := make([]any, 0, len(groups))
	for index, value := range groups {
		group, ok := value.(map[string]any)
		if !ok {
			cleaned = append(cleaned, value)
			continue
		}
		handlers, _ := group["hooks"].([]any)
		kept := handlersWithoutGuard(handlers)
		if len(kept) == len(handlers) {
			cleaned = append(cleaned, group)
			continue
		}
		if len(kept) > 0 {
			group["hooks"] = kept
			cleaned = append(cleaned, group)
			continue
		}
		// Keep empty placeholders only while later user groups need their index.
		if index < len(groups)-1 {
			group["hooks"] = []any{}
			cleaned = append(cleaned, group)
		}
	}
	if len(cleaned) == 0 {
		delete(hooks, event)
	} else {
		hooks[event] = cleaned
	}
}

func hooksFileIsDisposable(root map[string]any) bool {
	for key, value := range root {
		switch key {
		case "description":
			if value != "User hooks (includes Codex Session Guard)" {
				return false
			}
		case "hooks":
			hooks, ok := value.(map[string]any)
			if !ok || len(hooks) != 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func doctorMain() int {
	settings, err := loadSettings()
	if err != nil {
		fmt.Fprintln(os.Stderr, "[FAIL]", err)
		return 1
	}
	ok := true
	for _, name := range installedCommandNames() {
		path := filepath.Join(settings.InstallDir, name)
		if _, err := os.Stat(path); err != nil {
			fmt.Fprintln(os.Stderr, "[FAIL] Missing", name)
			ok = false
		} else if settings.BinaryHash != "" {
			hash, hashErr := fileSHA256(path)
			if hashErr != nil || !strings.EqualFold(hash, settings.BinaryHash) {
				fmt.Fprintln(os.Stderr, "[FAIL] Installed file does not match the recorded version:", name)
				ok = false
			}
		}
	}
	if ok {
		fmt.Println("[OK] csg and the hook-management entry point are installed")
	}
	if err := checkHookInstallation(settings); err != nil {
		fmt.Fprintln(os.Stderr, "[FAIL] Session hooks:", err)
		ok = false
	} else {
		fmt.Println("[OK] SessionStart and SessionEnd hooks are configured and explicitly trusted")
	}
	for _, base := range []string{"csg", "codex-session-guard"} {
		found, lookupErr := exec.LookPath(base)
		expected := filepath.Join(settings.InstallDir, commandName(base))
		if lookupErr != nil || !pathsEqual(found, expected) {
			fmt.Fprintln(os.Stderr, "[WARN] This shell does not resolve the expected entry point first:", commandName(base), "Update PATH and run doctor again.")
		} else {
			fmt.Println("[OK] PATH resolves the expected entry point first:", commandName(base))
		}
	}
	records, _ := recoveryCandidates()
	fmt.Printf("[INFO] Recoverable crashed sessions: %d\n", len(records))
	if !ok {
		return 1
	}
	return 0
}

func checkHookInstallation(settings Settings) error {
	data, err := os.ReadFile(filepath.Join(settings.CodexHome, "hooks.json"))
	if err != nil {
		return err
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	hooks, _ := root["hooks"].(map[string]any)
	hooksAbs, err := filepath.Abs(filepath.Join(settings.CodexHome, "hooks.json"))
	if err != nil {
		return err
	}
	configData, err := os.ReadFile(filepath.Join(settings.CodexHome, "config.toml"))
	if err != nil {
		return err
	}
	config := string(configData)
	if !strings.Contains(config, configBlockBegin) {
		return errors.New("explicit hook trust records are missing")
	}
	for _, spec := range guardHookSpecs {
		groups, _ := hooks[spec.Event].([]any)
		foundIndex := -1
		for index, group := range groups {
			if groupIsExpectedGuard(group, spec) {
				foundIndex = index
				break
			}
		}
		if foundIndex < 0 {
			return fmt.Errorf("hooks.json is missing the complete %s configuration", spec.Event)
		}
		expectedKey := fmt.Sprintf("%s:%s:%d:0", filepath.Clean(hooksAbs), spec.EventID, foundIndex)
		expectedHash := guardHookHash(spec)
		storedKey, storedHash := settings.HookKey, settings.HookHash
		if spec.Event == "SessionEnd" {
			storedKey, storedHash = settings.SessionEndHookKey, settings.SessionEndHookHash
		}
		if storedKey != expectedKey {
			return fmt.Errorf("the %s hook position changed and its trust index is no longer valid; reinstall Codex Session Guard", spec.Event)
		}
		if storedHash != expectedHash || !strings.Contains(config, strconv.Quote(expectedKey)) || !strings.Contains(config, strconv.Quote(expectedHash)) {
			return fmt.Errorf("the explicit %s trust record is missing or changed", spec.Event)
		}
	}
	return nil
}
