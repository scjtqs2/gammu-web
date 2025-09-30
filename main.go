package main

import (
	"flag"
	"github.com/ctaoist/gammu-web/config"
	"github.com/ctaoist/gammu-web/db"
	"github.com/ctaoist/gammu-web/smsd"
	"github.com/ctaoist/gammu-web/web"
	log "github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
	"io"
	"os"
)

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableColors:   false,
		ForceColors:     false,
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
	})
	log.SetLevel(log.InfoLevel)
	// 设置多输出
	var writers []io.Writer
	writers = append(writers, os.Stdout)
	logPath := os.Getenv("LOG_PATH")
	if logPath != "" {
		// 配置 lumberjack 日志轮转
		lumberjackLogger := &lumberjack.Logger{
			Filename:   logPath,
			MaxSize:    100,  // MB
			MaxBackups: 7,    // 保留的旧文件最大数量
			MaxAge:     30,   // 保留的最大天数
			Compress:   true, // 是否压缩/归档旧文件
			LocalTime:  true, // 使用本地时间
		}
		writers = append(writers, lumberjackLogger)
	}
	// 设置输出
	log.SetOutput(io.MultiWriter(writers...))
}

func main() {
	debug := flag.Bool("debug", false, "Debug mode")
	port := flag.String("port", "21234", "Server listen port")
	flag.BoolVar(&config.TestMode, "test", false, "Test mode, and not start gammu service")
	flag.StringVar(&config.AccessToken, "token", "", "Api access token")
	gammurc := flag.String("gammu-conf", "~/.gammurc", "Gammu config file")
	flag.StringVar(&config.LogFile, "log", "", "Log to file, default to stdout")
	flag.Parse()

	if *debug {
		log.SetLevel(log.DebugLevel)
	}

	if os.Getenv("API_TOKEN") != "" {
		config.AccessToken = os.Getenv("API_TOKEN")
	}
	if os.Getenv("API_PORT") != "" {
		*port = os.Getenv("API_PORT")
	}
	if os.Getenv("DEBUG") == "true" || os.Getenv("DEBUG") == "1" {
		log.SetLevel(log.DebugLevel)
	}

	if config.LogFile != "" {
		f, err := os.OpenFile(config.LogFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			log.Fatal("LogInit", err)
		}
		defer f.Close()
		log.SetOutput(f)
	}

	log.Info("Init", "Initializing...")
	db.Init()

	if !config.TestMode {
		smsd.Init(*gammurc)
		defer smsd.Close()
	}

	web.RunServer(*port)
}
