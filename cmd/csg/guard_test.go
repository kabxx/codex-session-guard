package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func useTempGuardHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CODEX_SESSION_GUARD_HOME", home)
	return home
}

func testdataFile(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(mustGetwd(), "..", "..", "testdata", name)
}

func TestRunRecordRoundTripAndBinding(t *testing.T) {
	useTempGuardHome(t)
	runID := "00112233445566778899aabbccddeeff"
	record := RunRecord{Version: 1, RunID: runID, StartedAt: time.Now().UTC(), CWD: `C:\work`}
	if err := saveNewRun(record); err != nil {
		t.Fatal(err)
	}
	input := HookInput{SessionID: "session-1", TranscriptPath: `C:\rollout.jsonl`, CWD: `C:\new`, Source: "startup"}
	if err := bindSession(runID, input); err != nil {
		t.Fatal(err)
	}
	got, err := loadRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != "session-1" || got.CWD != `C:\new` {
		t.Fatalf("unexpected record: %+v", got)
	}
}

func TestBindingFollowsClearSession(t *testing.T) {
	useTempGuardHome(t)
	runID := "10112233445566778899aabbccddeeff"
	if err := saveNewRun(RunRecord{Version: 1, RunID: runID, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := bindSession(runID, HookInput{SessionID: "before-clear", Source: "startup"}); err != nil {
		t.Fatal(err)
	}
	if err := bindSession(runID, HookInput{SessionID: "after-clear", Source: "clear"}); err != nil {
		t.Fatal(err)
	}
	record, err := loadRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if record.SessionID != "after-clear" || record.SessionSource != "clear" {
		t.Fatalf("clear did not replace the active session: %+v", record)
	}
}

func TestRecoverableRunsHidesLiveAndDeduplicates(t *testing.T) {
	useTempGuardHome(t)
	now := time.Now().UTC()
	currentStart, alive := processIdentity(os.Getpid())
	if !alive {
		t.Fatal("current process should be alive")
	}
	records := []RunRecord{
		{Version: 1, RunID: "00000000000000000000000000000001", StartedAt: now, SessionID: "live", CodexPID: os.Getpid(), CodexStartTime: currentStart},
		{Version: 1, RunID: "00000000000000000000000000000002", StartedAt: now.Add(-time.Minute), SessionID: "duplicate", CodexPID: 99999999},
		{Version: 1, RunID: "00000000000000000000000000000003", StartedAt: now.Add(-2 * time.Minute), SessionID: "duplicate", CodexPID: 99999999},
	}
	for _, record := range records {
		if err := saveNewRun(record); err != nil {
			t.Fatal(err)
		}
	}
	got, err := recoverableRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RunID != records[1].RunID {
		t.Fatalf("unexpected candidates: %+v", got)
	}
}

func TestTrackedSessionsIncludesStartingRunningAndCrashed(t *testing.T) {
	useTempGuardHome(t)
	now := time.Now().UTC()
	currentStart, alive := processIdentity(os.Getpid())
	if !alive {
		t.Fatal("current process should be alive")
	}
	records := []RunRecord{
		{Version: 1, RunID: "11000000000000000000000000000001", StartedAt: now, WrapperPID: os.Getpid(), WrapperStartTime: currentStart},
		{Version: 1, RunID: "11000000000000000000000000000002", StartedAt: now.Add(-time.Minute), SessionID: "running", WrapperPID: os.Getpid(), WrapperStartTime: currentStart},
		{Version: 1, RunID: "11000000000000000000000000000003", StartedAt: now.Add(-2 * time.Minute), SessionID: "crashed", WrapperPID: 99999999},
	}
	for _, record := range records {
		if err := saveNewRun(record); err != nil {
			t.Fatal(err)
		}
	}

	sessions, err := trackedSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 {
		t.Fatalf("tracked sessions = %+v", sessions)
	}
	want := []trackedStatus{statusStarting, statusRunning, statusCrashed}
	for index, status := range want {
		if sessions[index].Status != status {
			t.Fatalf("session %d status = %s, want %s", index, sessions[index].Status, status)
		}
	}

	recoverable, err := recoveryCandidates()
	if err != nil || len(recoverable) != 1 || recoverable[0].SessionID != "crashed" {
		t.Fatalf("recoverable = %+v, err = %v", recoverable, err)
	}
}

func TestRecoveryClaimPreventsConcurrentResume(t *testing.T) {
	useTempGuardHome(t)
	runID := "20112233445566778899aabbccddeeff"
	if err := saveNewRun(RunRecord{Version: 1, RunID: runID, StartedAt: time.Now().UTC(), SessionID: "claim-me"}); err != nil {
		t.Fatal(err)
	}
	if err := claimRun(runID); err != nil {
		t.Fatal(err)
	}
	if err := claimRun(runID); !errors.Is(err, errRecoveryInProgress) {
		t.Fatalf("second claim error = %v", err)
	}
	records, err := recoverableRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("claimed record was listed: %+v", records)
	}
	if err := releaseRunClaim(runID); err != nil {
		t.Fatal(err)
	}
	records, err = recoverableRuns()
	if err != nil || len(records) != 1 {
		t.Fatalf("released record missing: records=%+v err=%v", records, err)
	}
}

func TestUnboundDeadRecordIsDeleted(t *testing.T) {
	home := useTempGuardHome(t)
	runID := "30112233445566778899aabbccddeeff"
	if err := saveNewRun(RunRecord{Version: 1, RunID: runID, StartedAt: time.Now().Add(-time.Hour).UTC()}); err != nil {
		t.Fatal(err)
	}
	records, err := recoveryCandidates()
	if err != nil || len(records) != 0 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	if _, err := os.Stat(filepath.Join(home, "runs", runID+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unbound record was not deleted: %v", err)
	}
}

func TestRecoverySelectionRequiresSessionID(t *testing.T) {
	sessionID := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	records := []RunRecord{{SessionID: sessionID}}
	if _, ok := findRecordBySessionID(records, "1"); ok {
		t.Fatal("numeric list position was accepted as a session ID")
	}
	if record, ok := findRecordBySessionID(records, sessionID); !ok || record.SessionID != sessionID {
		t.Fatalf("session ID was not selected: record=%+v ok=%v", record, ok)
	}
}

func TestDeleteRecoveryRemovesEveryRecordForSessionID(t *testing.T) {
	useTempGuardHome(t)
	sessionID := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	records := []RunRecord{
		{Version: 1, RunID: "40112233445566778899aabbccddeeff", StartedAt: time.Now().UTC(), SessionID: sessionID},
		{Version: 1, RunID: "50112233445566778899aabbccddeeff", StartedAt: time.Now().Add(-time.Minute).UTC(), SessionID: sessionID},
		{Version: 1, RunID: "60112233445566778899aabbccddeeff", StartedAt: time.Now().UTC(), SessionID: "keep-me"},
	}
	for _, record := range records {
		if err := saveNewRun(record); err != nil {
			t.Fatal(err)
		}
	}

	if code := deleteMain([]string{sessionID}); code != 0 {
		t.Fatalf("delete returned %d, want 0", code)
	}
	got, err := readAllRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SessionID != "keep-me" {
		t.Fatalf("unexpected records after delete: %+v", got)
	}
}

func TestDeleteRecoveryRefusesLiveOrClaimedSession(t *testing.T) {
	useTempGuardHome(t)
	started, alive := processIdentity(os.Getpid())
	if !alive {
		t.Fatal("current process should be alive")
	}
	records := []RunRecord{
		{Version: 1, RunID: "70112233445566778899aabbccddeeff", StartedAt: time.Now().UTC(), SessionID: "live-session", WrapperPID: os.Getpid(), WrapperStartTime: started},
		{Version: 1, RunID: "80112233445566778899aabbccddeeff", StartedAt: time.Now().UTC(), SessionID: "claimed-session"},
	}
	for _, record := range records {
		if err := saveNewRun(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := claimRun(records[1].RunID); err != nil {
		t.Fatal(err)
	}

	if code := deleteMain([]string{"live-session"}); code == 0 {
		t.Fatal("delete accepted a live session")
	}
	if code := deleteMain([]string{"claimed-session"}); code == 0 {
		t.Fatal("delete accepted a claimed session")
	}
	for _, record := range records {
		if _, err := loadRun(record.RunID); err != nil {
			t.Fatalf("refused delete removed %s: %v", record.SessionID, err)
		}
	}
}

func TestDeleteRecoveryRevalidatesSessionIDUnderLock(t *testing.T) {
	useTempGuardHome(t)
	runID := "90112233445566778899aabbccddeeff"
	const oldSession = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	const newSession = "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff"
	if err := saveNewRun(RunRecord{Version: 1, RunID: runID, StartedAt: time.Now().UTC(), SessionID: oldSession}); err != nil {
		t.Fatal(err)
	}
	if err := updateRun(runID, func(record *RunRecord) { record.SessionID = newSession }); err != nil {
		t.Fatal(err)
	}
	if err := claimRunForDelete(runID, oldSession); !errors.Is(err, errDeleteSessionChanged) {
		t.Fatalf("claim after session change returned %v", err)
	}

	if err := updateRun(runID, func(record *RunRecord) { record.SessionID = oldSession }); err != nil {
		t.Fatal(err)
	}
	if err := claimRunForDelete(runID, oldSession); err != nil {
		t.Fatal(err)
	}
	if err := updateRun(runID, func(record *RunRecord) { record.SessionID = newSession }); err != nil {
		t.Fatal(err)
	}
	if err := removeClaimedRun(runID, oldSession); !errors.Is(err, errDeleteSessionChanged) {
		t.Fatalf("remove after session change returned %v", err)
	}
	if err := releaseRunClaim(runID); err != nil {
		t.Fatal(err)
	}
	record, err := loadRun(runID)
	if err != nil || record.SessionID != newSession {
		t.Fatalf("changed record was not preserved: record=%+v err=%v", record, err)
	}
}

func TestDeleteRecoveryDoesNotAcceptListPosition(t *testing.T) {
	useTempGuardHome(t)
	runID := "a0112233445566778899aabbccddeeff"
	if err := saveNewRun(RunRecord{Version: 1, RunID: runID, StartedAt: time.Now().UTC(), SessionID: "real-session-id"}); err != nil {
		t.Fatal(err)
	}
	if code := deleteMain([]string{"1"}); code != 2 {
		t.Fatalf("numeric list position returned %d, want 2", code)
	}
	if _, err := loadRun(runID); err != nil {
		t.Fatalf("numeric list position deleted a record: %v", err)
	}
}

func TestManagedBlockIsSurgical(t *testing.T) {
	config := "model = \"x\"\n\n" + configBlockBegin + "\n[hooks.state.\"key\"]\ntrusted_hash = \"hash\"\n" + configBlockEnd + "\n\n[features]\nhooks = true\n"
	clean, err := stripManagedBlock(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(clean, "Codex Session Guard") || !strings.Contains(clean, "model = \"x\"") || !strings.Contains(clean, "hooks = true") {
		t.Fatalf("unexpected result: %q", clean)
	}
}

func TestHookCoversSessionResetSourcesAndUninstallsCleanly(t *testing.T) {
	useTempGuardHome(t)
	codexStateRoot := t.TempDir()
	settings := Settings{CodexHome: codexStateRoot, HooksFileWasCreated: true}
	if err := installHook(&settings); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(codexStateRoot, "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), hookMatcher) {
		t.Fatalf("hook matcher missing reset sources: %s", data)
	}
	if !strings.Contains(string(data), `"SessionEnd"`) || settings.SessionEndHookKey == "" || settings.SessionEndHookHash == "" {
		t.Fatalf("SessionEnd hook or trust metadata missing: settings=%+v hooks=%s", settings, data)
	}
	if err := uninstallHook(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(codexStateRoot, "hooks.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tool-created hooks.json should be removed, err=%v", err)
	}
}

func TestInstallHookMigratesLegacyWindowsCommand(t *testing.T) {
	useTempGuardHome(t)
	codexStateRoot := t.TempDir()
	hooks := `{"hooks":{"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"codex-session-guard.exe hook","timeout":10,"async":false}]}]}}`
	if err := os.WriteFile(filepath.Join(codexStateRoot, "hooks.json"), []byte(hooks), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := Settings{CodexHome: codexStateRoot}
	if err := installHook(&settings); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(codexStateRoot, "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), legacyHookCommand) || strings.Count(string(data), hookCommand) != 2 {
		t.Fatalf("legacy hook was not migrated exactly once per event: %s", data)
	}
}

func TestForeignInstallTargetIsRejected(t *testing.T) {
	useTempGuardHome(t)
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, commandName("csg")), []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateInstallTargets(binDir); err == nil {
		t.Fatal("foreign csg command was accepted")
	}
}

func TestForeignCodexIsNotAnInstallTarget(t *testing.T) {
	useTempGuardHome(t)
	binDir := t.TempDir()
	path := filepath.Join(binDir, commandName("codex"))
	if err := os.WriteFile(path, []byte("upstream"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateInstallTargets(binDir); err != nil {
		t.Fatalf("upstream codex was treated as a CSG install target: %v", err)
	}
}

func TestInstallTargetsMustMatchRecordedHash(t *testing.T) {
	useTempGuardHome(t)
	binDir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range installedCommandNames() {
		if err := copyExecutable(self, filepath.Join(binDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	settingsFile, err := settingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(settingsFile, Settings{Version: 2, InstallDir: binDir, BinaryHash: "sha256:does-not-match"}); err != nil {
		t.Fatal(err)
	}
	if err := validateInstallTargets(binDir); err == nil || !strings.Contains(err.Error(), "settings.json") {
		t.Fatalf("mismatched recorded hash was accepted: %v", err)
	}
}

func TestCSGRequiresExplicitSubcommand(t *testing.T) {
	if code := csgMain([]string{"spine-codex.cmd"}); code != 2 {
		t.Fatalf("legacy csg <launcher> syntax returned %d, want 2", code)
	}
	if code := csgMain([]string{"run"}); code != 2 {
		t.Fatalf("csg run without a launcher returned %d, want 2", code)
	}
	if code := csgMain([]string{"list", "extra"}); code != 2 {
		t.Fatalf("csg list with an argument returned %d, want 2", code)
	}
	if code := csgMain([]string{"resume"}); code != 2 {
		t.Fatalf("csg resume without a session ID returned %d, want 2", code)
	}
	if code := csgMain([]string{"delete"}); code != 2 {
		t.Fatalf("csg delete without a session ID returned %d, want 2", code)
	}
	if code := csgMain([]string{"delete", "one", "two"}); code != 2 {
		t.Fatalf("csg delete with extra arguments returned %d, want 2", code)
	}
	if code := csgMain([]string{"delete", "--all"}); code != 2 {
		t.Fatalf("csg delete --all returned %d, want 2", code)
	}
	if code := csgMain([]string{"delete", "--help", "extra"}); code != 2 {
		t.Fatalf("csg delete help with extra arguments returned %d, want 2", code)
	}
}

func TestLegacyCommandOwnershipUsesRecordedHash(t *testing.T) {
	useTempGuardHome(t)
	binDir := t.TempDir()
	legacyPaths := []string{
		filepath.Join(binDir, commandName("codex")),
		filepath.Join(binDir, commandName("codex-recover")),
	}
	for _, path := range legacyPaths {
		if err := os.WriteFile(path, []byte("owned"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	hash, err := fileSHA256(legacyPaths[0])
	if err != nil {
		t.Fatal(err)
	}
	settingsFile, err := settingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(settingsFile, Settings{Version: 1, InstallDir: binDir, RealCodexPath: legacyPaths[0], BinaryHash: hash}); err != nil {
		t.Fatal(err)
	}
	owned, err := ownedLegacyCommands(binDir)
	if err != nil || len(owned) != 2 {
		t.Fatalf("owned=%v err=%v", owned, err)
	}
	if err := os.WriteFile(legacyPaths[0], []byte("foreign"), 0o700); err != nil {
		t.Fatal(err)
	}
	owned, err = ownedLegacyCommands(binDir)
	if err != nil || len(owned) != 1 || !pathsEqual(owned[0], legacyPaths[1]) {
		t.Fatalf("foreign legacy command ownership=%v err=%v", owned, err)
	}
}

func TestWriteJSONAtomicReplacesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "value.json")
	if err := writeJSONAtomic(path, map[string]int{"value": 1}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(path, map[string]int{"value": 2}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "2") {
		t.Fatalf("unexpected data: %s", data)
	}
}
