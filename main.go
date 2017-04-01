package main

import (
	cryptoRand "crypto/rand"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"github.com/HouzuoGuo/laitos/global"
	"io/ioutil"
	pseudoRand "math/rand"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var logger = global.Logger{ComponentName: "laitos", ComponentID: strconv.Itoa(os.Getpid())}

// Re-seed global pseudo random generator using cryptographic random number generator.
func ReseedPseudoRand() {
	numAttempts := 1
	for ; ; numAttempts++ {
		seedBytes := make([]byte, 8)
		_, err := cryptoRand.Read(seedBytes)
		if err != nil {
			logger.Panicf("ReseedPseudoRand", "", err, "failed to read from random generator")
		}
		seed, _ := binary.Varint(seedBytes)
		if seed == 0 {
			// If random entropy decodes into an integer that overflows, simply retry.
			continue
		} else {
			pseudoRand.Seed(seed)
			break
		}
	}
	logger.Printf("ReseedPseudoRand", "", nil, "succeeded after %d attempt(s)", numAttempts)
}

// Stop and disable daemons that may run into port usage conflicts with laitos.
func StopConflictingDaemons() {
	if os.Getuid() != 0 {
		logger.Fatalf("StopConflictingDaemons", "", nil, "you must run laitos as root user if you wish to automatically disable conflicting daemons")
	}
	list := []string{"apache", "apache2", "bind", "bind9", "httpd", "lighttpd", "named", "nginx", "postfix", "sendmail"}
	waitGroup := new(sync.WaitGroup)
	waitGroup.Add(len(list))
	for _, name := range list {
		go func(name string) {
			defer waitGroup.Done()
			var success bool
			// Disable+stop intensifies three times...
			for i := 0; i < 3; i++ {
				// Some hosting platforms out there still have not yet used systemd
				cmds := []*exec.Cmd{
					exec.Command("/etc/init.d/"+name, "stop"),
					exec.Command("chkconfig", name, "off"),
					exec.Command("chmod", "0000", "/etc/init,d/"+name),
					exec.Command("systemctl", "stop", name),
					exec.Command("systemctl", "disable", name),
					exec.Command("systemctl", "mask", name),
				}
				for _, cmd := range cmds {
					if _, err := cmd.CombinedOutput(); err == nil {
						success = true
						// Continue to run subsequent commands to further disable the service
					}
				}
				time.Sleep(1 * time.Second)
			}
			if success {
				logger.Printf("StopConflictingDaemons", name, nil, "the daemon has been successfully stopped and disabled")
			}
		}(name)
	}
	waitGroup.Wait()
}

// A daemon that starts and blocks.
type Daemon interface {
	StartAndBlock() error
}

// Start a daemon in a separate goroutine. If the daemon crashes, the goroutine logs an error message but does not crash the entire program.
func StartDaemon(counter *int32, waitGroup *sync.WaitGroup, name string, daemon Daemon) {
	atomic.AddInt32(counter, 1)
	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		defer func() {
			if err := recover(); err != nil {
				logger.Printf("main", name, errors.New(fmt.Sprint(err)), "daemon crashed!")
			}
		}()
		logger.Printf("main", name, nil, "going to start daemon")
		if err := daemon.StartAndBlock(); err != nil {
			logger.Printf("main", name, err, "daemon has failed")
			return
		}
	}()
}

func main() {
	// Lock all program memory into main memory to prevent sensitive data from leaking into swap.
	if os.Geteuid() == 0 {
		if err := syscall.Mlockall(syscall.MCL_CURRENT | syscall.MCL_FUTURE); err != nil {
			logger.Fatalf("main", "", err, "failed to lock memory")
			return
		}
		logger.Printf("main", "", nil, "program has been locked into memory for safety reasons")
	} else {
		logger.Printf("main", "", nil, "program is not running as root (UID 0) hence memory is not locked, your private information will leak into swap.")
	}

	// Re-seed pseudo random number generator once a while
	ReseedPseudoRand()
	go func() {
		ReseedPseudoRand()
		time.Sleep(2 * time.Minute)
	}()

	// Process command line flags
	var configFile, frontend string
	var conflictFree bool
	flag.StringVar(&configFile, "config", "", "(Mandatory) path to configuration file in JSON syntax")
	flag.StringVar(&frontend, "frontend", "", "(Mandatory) comma-separated frontend services to start (dnsd, healthcheck, httpd, httpd80, mailp, smtpd, sockd, telegram)")
	flag.BoolVar(&conflictFree, "conflictfree", false, "(Optional) automatically stop and disable system daemons that may run into port conflict with laitos")
	flag.Parse()

	if configFile == "" {
		logger.Fatalf("main", "", nil, "please provide a configuration file (-config)")
		return
	}
	frontendList := regexp.MustCompile(`\w+`)
	frontends := frontendList.FindAllString(frontend, -1)
	if frontends == nil || len(frontends) == 0 {
		logger.Fatalf("main", "", nil, "please provide comma-separated list of frontend services to start (-frontend).")
		return
	}

	// Deserialise configuration file
	var config Config
	configBytes, err := ioutil.ReadFile(configFile)
	if err != nil {
		logger.Fatalf("main", "", err, "failed to read config file \"%s\"", configFile)
		return
	}
	if err := config.DeserialiseFromJSON(configBytes); err != nil {
		logger.Fatalf("main", "", err, "failed to deserialise config file \"%s\"", configFile)
		return
	}

	// Start frontent daemons
	if conflictFree {
		StopConflictingDaemons()
	}
	waitGroup := &sync.WaitGroup{}
	var numDaemons int32
	for _, frontendName := range frontends {
		switch frontendName {
		case "dnsd":
			StartDaemon(&numDaemons, waitGroup, frontendName, config.GetDNSD())
		case "healthcheck":
			StartDaemon(&numDaemons, waitGroup, frontendName, config.GetHealthCheck())
		case "httpd":
			StartDaemon(&numDaemons, waitGroup, frontendName, config.GetHTTPD())
		case "httpd80":
			StartDaemon(&numDaemons, waitGroup, frontendName, config.GetHTTPD80())
		case "mailp":
			mailContent, err := ioutil.ReadAll(os.Stdin)
			if err != nil {
				logger.Fatalf("main", "", err, "failed to read mail from STDIN")
				return
			}
			if err := config.GetMailProcessor().Process(mailContent); err != nil {
				logger.Fatalf("main", "", err, "failed to process mail")
			}
		case "smtpd":
			StartDaemon(&numDaemons, waitGroup, frontendName, config.GetMailDaemon())
		case "sockd":
			StartDaemon(&numDaemons, waitGroup, frontendName, config.GetSockDaemon())
		case "telegram":
			StartDaemon(&numDaemons, waitGroup, frontendName, config.GetTelegramBot())
		default:
			logger.Fatalf("main", "", err, "unknown frontend name \"%s\"", frontendName)
		}
	}
	if numDaemons > 0 {
		logger.Printf("main", "", nil, "started %d daemons", numDaemons)
	}
	// Daemons are not really supposed to quit
	waitGroup.Wait()
}
