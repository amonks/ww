package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Workspace Workspace `toml:"workspace"`
}

type Workspace struct {
	OnCreate  string `toml:"on-create"`
	OnAcquire string `toml:"on-acquire"`
}

func Load(repoPath string) (*Config, error) {
	globalPath, err := globalConfigPath()
	if err != nil {
		return nil, err
	}
	globalCfg, globalMeta, err := loadConfigFile(globalPath)
	if err != nil {
		return nil, err
	}
	projectCfg, projectMeta, err := loadProjectConfig(repoPath)
	if err != nil {
		return nil, err
	}
	merged := mergeConfigs(globalCfg, projectCfg, globalMeta, projectMeta)
	return merged, nil
}

func loadProjectConfig(repoPath string) (*Config, toml.MetaData, error) {
	rootPath := filepath.Join(repoPath, "ww.toml")
	altPath := filepath.Join(repoPath, ".ww", "config.toml")

	rootExists, err := fileExists(rootPath)
	if err != nil {
		return nil, toml.MetaData{}, fmt.Errorf("check project config %s: %w", rootPath, err)
	}
	altExists, err := fileExists(altPath)
	if err != nil {
		return nil, toml.MetaData{}, fmt.Errorf("check project config %s: %w", altPath, err)
	}
	if rootExists && altExists {
		return nil, toml.MetaData{}, fmt.Errorf("project config files both exist: %s and %s", rootPath, altPath)
	}
	if rootExists {
		return loadConfigFile(rootPath)
	}
	if altExists {
		return loadConfigFile(altPath)
	}
	return &Config{}, toml.MetaData{}, nil
}

func fileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err == nil {
		return !info.IsDir(), nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func globalConfigPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home directory: %w", err)
	}
	return filepath.Join(homeDir, ".config", "ww", "config.toml"), nil
}

func loadConfigFile(path string) (*Config, toml.MetaData, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Config{}, toml.MetaData{}, nil
	}
	if err != nil {
		return nil, toml.MetaData{}, fmt.Errorf("read config file %s: %w", path, err)
	}
	var cfg Config
	meta, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return nil, toml.MetaData{}, fmt.Errorf("parse config file %s: %w", path, err)
	}
	return &cfg, meta, nil
}

func mergeConfigs(globalCfg, projectCfg *Config, globalMeta, projectMeta toml.MetaData) *Config {
	if globalCfg == nil {
		globalCfg = &Config{}
	}
	if projectCfg == nil {
		projectCfg = &Config{}
	}
	merged := Config{}
	merged.Workspace.OnCreate = mergeString(projectMeta.IsDefined("workspace", "on-create"), projectCfg.Workspace.OnCreate, globalCfg.Workspace.OnCreate)
	merged.Workspace.OnAcquire = mergeString(projectMeta.IsDefined("workspace", "on-acquire"), projectCfg.Workspace.OnAcquire, globalCfg.Workspace.OnAcquire)
	return &merged
}

func mergeString(projectDefined bool, projectValue, globalValue string) string {
	value := globalValue
	if projectDefined {
		value = projectValue
	}
	return strings.TrimSpace(value)
}

func RunScript(dir, script string) error {
	script = strings.TrimSpace(script)
	if script == "" {
		return nil
	}
	var interpreter string
	scriptBody := ""
	if strings.HasPrefix(script, "#!") {
		lines := strings.SplitN(script, "\n", 2)
		interpreter = strings.TrimSpace(strings.TrimPrefix(lines[0], "#!"))
		if len(lines) > 1 {
			scriptBody = lines[1]
		}
	} else {
		interpreter = "/bin/bash"
		scriptBody = script
	}
	parts := strings.Fields(interpreter)
	if len(parts) == 0 {
		return fmt.Errorf("empty interpreter in shebang")
	}
	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(scriptBody)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
