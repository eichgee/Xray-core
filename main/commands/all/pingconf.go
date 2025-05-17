package all

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/xtls/xray-core/common/cmdarg"
	"github.com/xtls/xray-core/common/errors"
	v2net "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/platform"
	"github.com/xtls/xray-core/common/serial"
	core "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/main/commands/base"
)

var cmdPing = &base.Command{
	UsageLine: `{{.Exec}} ping [-u https://www.google.com/] [-c config.json] [-t 10s]`,
	Short:     `Ping Xray with config and exit`,
	Long: `
The -config=file, -c=file flags set the config files for 
Xray. Multiple assign is accepted.

The -confdir=dir flag sets a dir with multiple json config

The -format=json flag sets the format of config files. 
Default "auto".

The -u for target url of ping

The -t for connection timeout
`,
}

func init() {
	cmdPing.Run = executePing // break init loop
}

var (
	configFiles cmdarg.Arg
	configDir   string
	format      = cmdPing.Flag.String("format", "auto", "Format of input file.")
	pingUrl     = cmdPing.Flag.String("u", "https://www.google.com/", "")
	pingTimeout = cmdPing.Flag.String("t", "10s", "")

	_ = func() bool {
		cmdPing.Flag.Var(&configFiles, "config", "Config path for Xray.")
		cmdPing.Flag.Var(&configFiles, "c", "Short alias of -config")
		cmdPing.Flag.StringVar(&configDir, "confdir", "", "A dir with multiple json config")

		return true
	}()
)

func executePing(cmd *base.Command, args []string) {
	output, err := measureOutboundDelay()
	if err != nil {
		fmt.Println("error: ", err)
		os.Exit(-1)
	}
	fmt.Println(output)
	os.Exit(0)
}

func fileExists(file string) bool {
	info, err := os.Stat(file)
	return err == nil && !info.IsDir()
}

func dirExists(file string) bool {
	if file == "" {
		return false
	}
	info, err := os.Stat(file)
	return err == nil && info.IsDir()
}

func getRegepxByFormat() string {
	switch strings.ToLower(*format) {
	case "json":
		return `^.+\.(json|jsonc)$`
	case "toml":
		return `^.+\.toml$`
	case "yaml", "yml":
		return `^.+\.(yaml|yml)$`
	default:
		return `^.+\.(json|jsonc|toml|yaml|yml)$`
	}
}

func readConfDir(dirPath string) {
	confs, err := os.ReadDir(dirPath)
	if err != nil {
		log.Fatalln(err)
	}
	for _, f := range confs {
		matched, err := regexp.MatchString(getRegepxByFormat(), f.Name())
		if err != nil {
			log.Fatalln(err)
		}
		if matched {
			configFiles.Set(path.Join(dirPath, f.Name()))
		}
	}
}

func getConfigFilePath(verbose bool) cmdarg.Arg {
	if dirExists(configDir) {
		if verbose {
			log.Println("Using confdir from arg:", configDir)
		}
		readConfDir(configDir)
	} else if envConfDir := platform.GetConfDirPath(); dirExists(envConfDir) {
		if verbose {
			log.Println("Using confdir from env:", envConfDir)
		}
		readConfDir(envConfDir)
	}

	if len(configFiles) > 0 {
		return configFiles
	}

	if workingDir, err := os.Getwd(); err == nil {
		configFile := filepath.Join(workingDir, "config.json")
		if fileExists(configFile) {
			if verbose {
				log.Println("Using default config: ", configFile)
			}
			return cmdarg.Arg{configFile}
		}
	}

	if configFile := platform.GetConfigurationPath(); fileExists(configFile) {
		if verbose {
			log.Println("Using config from env: ", configFile)
		}
		return cmdarg.Arg{configFile}
	}

	if verbose {
		log.Println("Using config from STDIN")
	}
	return cmdarg.Arg{"stdin:"}
}

func getConfigFormat() string {
	f := core.GetFormatByExtension(*format)
	if f == "" {
		f = "auto"
	}
	return f
}

func measureOutboundDelay() (string, error) {
	configFiles := getConfigFilePath(true)

	config, err := core.LoadConfig(getConfigFormat(), configFiles)
	if err != nil {
		return "", errors.New("failed to load config files: [", configFiles.String(), "]").Base(err)
	}

	log := &conf.LogConfig{LogLevel: "none"}
	logSerial := serial.ToTypedMessage(log.Build())

	for i, a := range config.App {
		if strings.Compare(a.Type, logSerial.Type) == 0 {
			config.App[i] = logSerial
		}
	}
	config.Inbound = nil

	inst, err := core.New(config)
	if err != nil {
		return "", err
	}

	err = inst.Start()
	if err != nil {
		return "", err
	}
	delay, err := measureInstDelay(context.Background(), inst, *pingUrl, *pingTimeout)
	inst.Close()
	return delay, err
}

func measureInstDelay(ctx context.Context, inst *core.Instance, url string, timeout string) (string, error) {
	connectTimeout, err := time.ParseDuration(timeout)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Add("Connection", "close")
	req.Header.Add("Accept-Encoding", "gzip")

	tr := &http.Transport{
		TLSHandshakeTimeout: connectTimeout,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dest, err := v2net.ParseDestination(fmt.Sprintf("%s:%s", network, addr))
			if err != nil {
				return nil, err
			}
			return core.Dial(ctx, inst, dest)
		},
	}

	c := &http.Client{
		Transport: tr,
		Timeout:   connectTimeout,
	}

	start := time.Now()
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	timeElapsed := time.Since(start).Milliseconds()

	httpStatus := resp.Status

	resp.Body.Close()

	return fmt.Sprintf("ret_msg:%s,ret_time:%d", httpStatus, timeElapsed), nil
}