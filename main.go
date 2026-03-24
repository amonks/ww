// Package main implements the ww CLI tool for workspace pool management.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"monks.co/ww/ww"
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		var exitErr interface{ ExitCode() int }
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "ww",
	Short: "Workspace pool management for jujutsu repositories",
}

// Flag variables.
var (
	acquireRev     string
	acquirePurpose string
	execRev        string
	execPurpose    string
	listJSON       bool
	listAll        bool
)

var acquireCmd = &cobra.Command{
	Use:   "acquire",
	Short: "Acquire an available workspace or create a new one",
	RunE:  runAcquire,
}

var releaseCmd = &cobra.Command{
	Use:   "release [name...]",
	Short: "Release one or more acquired workspaces back to the pool",
	RunE:  runRelease,
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all workspaces for the current repo",
	RunE:  runList,
}

var execCmd = &cobra.Command{
	Use:          "exec [flags] -- <command> [args...]",
	Short:        "Run a command in an acquired workspace",
	Args:         cobra.MinimumNArgs(1),
	SilenceUsage: true,
	RunE:         runExec,
}

var destroyAllCmd = &cobra.Command{
	Use:   "destroy-all",
	Short: "Destroy all workspaces for the current repository",
	RunE:  runDestroyAll,
}

func init() {
	rootCmd.AddCommand(acquireCmd, releaseCmd, listCmd, execCmd, destroyAllCmd)

	acquireCmd.Flags().StringVar(&acquireRev, "rev", "@", "Revision to base the new change on")
	acquireCmd.Flags().StringVar(&acquirePurpose, "purpose", "", "Purpose for acquiring the workspace")
	execCmd.Flags().StringVar(&execRev, "rev", "@", "Revision to base the new change on")
	execCmd.Flags().StringVar(&execPurpose, "purpose", "", "Purpose for acquiring the workspace (defaults to \"exec: <command>\")")
	listCmd.Flags().BoolVar(&listJSON, "json", false, "Output as JSON")
	listCmd.Flags().BoolVar(&listAll, "all", false, "Show all workspaces including non-active ones")
}

// openPool opens the workspace pool using default options.
func openPool() (*ww.Pool, error) {
	pool, err := ww.Open()
	if err != nil {
		return nil, err
	}
	return pool, nil
}

// getRepoPath returns the jj repository root for the current directory.
func getRepoPath() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	return ww.RepoRoot(cwd)
}

// resolveWorkspaceName returns the workspace name from args or current directory.
func resolveWorkspaceName(pool *ww.Pool) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	return pool.WorkspaceNameForPath(cwd)
}

func runAcquire(cmd *cobra.Command, args []string) error {
	if err := ww.ValidateAcquirePurpose(acquirePurpose); err != nil {
		return err
	}

	pool, err := openPool()
	if err != nil {
		return err
	}
	defer pool.Close()

	repoPath, err := getRepoPath()
	if err != nil {
		return err
	}

	wsPath, err := pool.Acquire(repoPath, ww.AcquireOptions{
		Rev:     acquireRev,
		Purpose: acquirePurpose,
	})
	if err != nil {
		return fmt.Errorf("acquire workspace: %w", err)
	}

	fmt.Println(wsPath)
	return nil
}

func runRelease(cmd *cobra.Command, args []string) error {
	pool, err := openPool()
	if err != nil {
		return err
	}
	defer pool.Close()

	repoPath, err := getRepoPath()
	if err != nil {
		return err
	}

	// If no args provided, resolve from current directory.
	if len(args) == 0 {
		wsName, err := resolveWorkspaceName(pool)
		if err != nil {
			return err
		}
		args = []string{wsName}
	}

	for _, wsName := range args {
		if err := pool.ReleaseByName(repoPath, wsName); err != nil {
			return err
		}
		fmt.Printf("released workspace %s\n", wsName)
	}
	return nil
}

func runList(cmd *cobra.Command, args []string) error {
	pool, err := openPool()
	if err != nil {
		return err
	}
	defer pool.Close()

	repoPath, err := getRepoPath()
	if err != nil {
		return err
	}

	items, err := pool.List(repoPath)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}

	items = filterWorkspaceList(items, listAll)

	if listJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(items)
	}

	if len(items) == 0 {
		fmt.Println("No workspaces found for this repository.")
		return nil
	}

	fmt.Print(formatWorkspaceTable(items, time.Now()))
	return nil
}

func filterWorkspaceList(items []ww.Info, includeAll bool) []ww.Info {
	if includeAll {
		return items
	}

	filtered := make([]ww.Info, 0, len(items))
	for _, item := range items {
		switch item.Status {
		case ww.StatusAcquired, ww.StatusAvailable:
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func runExec(cmd *cobra.Command, args []string) error {
	pool, err := openPool()
	if err != nil {
		return err
	}
	defer pool.Close()

	repoPath, err := getRepoPath()
	if err != nil {
		return err
	}

	purpose := execPurpose
	if purpose == "" {
		purpose = "exec: " + args[0]
	}

	wsPath, err := pool.Acquire(repoPath, ww.AcquireOptions{
		Rev:     execRev,
		Purpose: purpose,
	})
	if err != nil {
		return fmt.Errorf("acquire workspace: %w", err)
	}
	defer pool.Release(wsPath)

	c := exec.Command(args[0], args[1:]...)
	c.Dir = wsPath

	// Use a PTY so interactive programs see a terminal.
	ptmx, err := pty.Start(c)
	if err != nil {
		return fmt.Errorf("start command: %w", err)
	}
	defer ptmx.Close()

	// Handle window size changes.
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			_ = pty.InheritSize(os.Stdin, ptmx)
		}
	}()
	// Set initial size.
	_ = pty.InheritSize(os.Stdin, ptmx)

	// Put stdin into raw mode so keystrokes pass through.
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err == nil {
		defer term.Restore(int(os.Stdin.Fd()), oldState)
	}

	// Copy stdin->pty and pty->stdout concurrently.
	outDone := make(chan struct{})
	go func() {
		copyIO(ptmx, os.Stdin)
	}()
	go func() {
		defer close(outDone)
		copyIO(os.Stdout, ptmx)
	}()

	// Wait for the command to finish.
	cmdErr := c.Wait()

	// Wait for all output to be copied before returning.
	<-outDone

	// Stop relaying signals.
	signal.Stop(ch)
	close(ch)

	if cmdErr != nil {
		var exitErr *exec.ExitError
		if errors.As(cmdErr, &exitErr) {
			return exitError{code: exitErr.ExitCode()}
		}
		return cmdErr
	}
	return nil
}

func copyIO(dst *os.File, src *os.File) (int64, error) {
	buf := make([]byte, 32*1024)
	var written int64
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			nw, writeErr := dst.Write(buf[:n])
			written += int64(nw)
			if writeErr != nil {
				return written, writeErr
			}
		}
		if readErr != nil {
			return written, readErr
		}
	}
}

func runDestroyAll(cmd *cobra.Command, args []string) error {
	pool, err := openPool()
	if err != nil {
		return err
	}
	defer pool.Close()

	repoPath, err := getRepoPath()
	if err != nil {
		return err
	}

	return pool.DestroyAll(repoPath)
}

// exitError wraps an exit code for propagation through cobra.
type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return fmt.Sprintf("exit %d", e.code)
}

func (e exitError) ExitCode() int {
	return e.code
}

func (e exitError) Unwrap() error {
	return e.err
}

// Table formatting helpers.

func formatWorkspaceTable(items []ww.Info, now time.Time) string {
	headers := []string{"NAME", "STATUS", "AGE", "DURATION", "REV", "PURPOSE", "PATH"}
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		purpose := item.Purpose
		if purpose == "" {
			purpose = "-"
		}

		rev := item.Rev
		if rev == "" {
			rev = "-"
		}

		age := formatWorkspaceAge(item, now)
		duration := formatWorkspaceDuration(item, now)
		rows = append(rows, []string{
			item.Name,
			string(item.Status),
			age,
			duration,
			rev,
			truncateTableCell(purpose),
			truncateTableCell(item.Path),
		})
	}

	return formatTable(headers, rows)
}

func formatWorkspaceAge(item ww.Info, now time.Time) string {
	if item.CreatedAt.IsZero() {
		return "-"
	}
	return formatDurationShort(now.Sub(item.CreatedAt))
}

func formatWorkspaceDuration(item ww.Info, now time.Time) string {
	if item.CreatedAt.IsZero() {
		return "-"
	}
	if item.Status == ww.StatusAcquired {
		return formatDurationShort(now.Sub(item.CreatedAt))
	}
	if item.UpdatedAt.IsZero() {
		return "-"
	}
	return formatDurationShort(item.UpdatedAt.Sub(item.CreatedAt))
}

// formatDurationShort returns a compact human-readable duration like "5m", "2h", "3d".
func formatDurationShort(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// formatTable renders a simple column-aligned text table.
func formatTable(headers []string, rows [][]string) string {
	if len(headers) == 0 {
		return ""
	}

	// Calculate column widths.
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}

	var sb strings.Builder

	// Header row.
	for i, h := range headers {
		if i > 0 {
			sb.WriteString("  ")
		}
		sb.WriteString(h)
		if i < len(headers)-1 {
			sb.WriteString(strings.Repeat(" ", widths[i]-len(h)))
		}
	}
	sb.WriteString("\n")

	// Data rows.
	for _, row := range rows {
		for i, cell := range row {
			if i >= len(widths) {
				break
			}
			if i > 0 {
				sb.WriteString("  ")
			}
			sb.WriteString(cell)
			if i < len(row)-1 && i < len(widths)-1 {
				sb.WriteString(strings.Repeat(" ", widths[i]-len(cell)))
			}
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// truncateTableCell truncates strings longer than 40 characters.
func truncateTableCell(s string) string {
	const maxLen = 40
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
