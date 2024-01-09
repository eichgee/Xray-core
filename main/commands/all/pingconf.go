package all

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
	v2net "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/serial"
	v2core "github.com/xtls/xray-core/core"
	v2serial "github.com/xtls/xray-core/infra/conf/serial"
	"github.com/xtls/xray-core/main/commands/base"
    "github.com/xtls/xray-core/infra/conf"
)

var cmdPing = &base.Command{
	UsageLine: `{{.Exec}} ping [-u https://www.google.com/] [-c "C:\conf.json"] [-t 10s]`,
	Short:     `Ping config and exit`,
	Long: `
	-u for target url of ping
	-c xray file config
	-t for timeout
`,
}

func init() {
	cmdPing.Run = executePing // break init loop
}

var(
	pingConfig = cmdPing.Flag.String("c", "", "")
	pingUrl = cmdPing.Flag.String("u", "https://www.google.com/", "")
	pingTimeout = cmdPing.Flag.String("t", "10s", "")
)

func executePing(cmd *base.Command, args []string) {
	lenConfig := len(*pingConfig)

	isValid := lenConfig > 0
	if (isValid){
		output, err:= measureOutboundDelay(*pingConfig, *pingUrl, *pingTimeout)
		if err != nil{
			fmt.Printf("error: %v", err)
			return
		}
		fmt.Println(output)
	}else{
		cmdPing.Usage()
	}
}

func measureOutboundDelay(filePath string, Url string, timeout string) (string, error) {
	bytes, err := os.ReadFile(filePath)
		if err !=nil{
			fmt.Println(err)
			return "", err
		}
	config, err := v2serial.LoadJSONConfig(strings.NewReader(string(bytes)))
	if err != nil {
		return "", err
	}

	log := &conf.LogConfig{LogLevel: "none"}
	logSerial := serial.ToTypedMessage(log.Build())

	for i, a := range config.App{
		if strings.Compare(a.Type, logSerial.Type) == 0{
			config.App[i] = logSerial
		}
	}
	config.Inbound = nil

	inst, err := v2core.New(config)
	if err != nil {
		return "", err
	}

	err = inst.Start()
	if err != nil {
		return "", err
	}
	delay, err := measureInstDelay(context.Background(), inst, Url, timeout)
	inst.Close()
	return delay, err
}

func measureInstDelay(ctx context.Context, inst *v2core.Instance, url string, timeout string) (string, error) {
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
			return v2core.Dial(ctx, inst, dest)
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