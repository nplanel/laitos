package maintenance

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/HouzuoGuo/laitos/inet"
	"github.com/HouzuoGuo/laitos/misc"
	"github.com/HouzuoGuo/laitos/platform"
)

// If package manager output contains any of the strings, the procedure output about the package will be reduced into "Nothing to do"
var suppressOutputMarkers = []string{"no packages marked for update", "nothing to do", "not found", "0 upgraded, 0 newly installed", "unable to locate", "already installed", "is the latest version"}

/*
PrepareDockerRepositorForDebian prepares APT repository for installing docker, because debian does not distribute docker
in their repository for whatever reason. If the system is not a debian the function will do nothing.

The software maintenance routine runs this function prior to installing the set of useful system software packages,
among which there is "add-apt-repository" command. Hence, on a freshly provisioned Debian system the command may not be
available, which causes docker to be missing even after system maintenance routine has run for the first time. The fault
will correct itself when system maintenance routine runs a second time.
*/
func (daemon *Daemon) prepareDockerRepositoryForDebian(out *bytes.Buffer) {
	if misc.HostIsWindows() {
		daemon.logPrintStageStep(out, "skipped on windows: prepare docker repository for debian system")
		return
	}
	daemon.logPrintStageStep(out, "prepare docker repository for debian system")
	content, err := ioutil.ReadFile("/etc/os-release")
	if err != nil {
		daemon.logPrintStageStep(out, "failed to read os-release, skip rest of the stage.")
		return
	} else if !strings.Contains(strings.ToLower(string(content)), "debian") || strings.Contains(strings.ToLower(string(content)), "ubuntu") {
		daemon.logPrintStageStep(out, "system is not a debian, skip rest of the stage.")
		return
	}
	// Install docker's GPG key
	resp, err := inet.DoHTTP(inet.HTTPRequest{}, "https://download.docker.com/linux/debian/gpg")
	if err != nil {
		daemon.logPrintStageStep(out, "failed to download docker GPG key - %v", err)
		return
	}
	gpgKeyFile := "/tmp/laitos-docker-gpg-key"
	if err := ioutil.WriteFile(gpgKeyFile, resp.Body, 0600); err != nil {
		daemon.logPrintStageStep(out, "failed to store docker GPG key - %v", err)
		return
	}
	aptOut, err := platform.InvokeProgram(nil, misc.CommonOSCmdTimeoutSec, "apt-key", "add", gpgKeyFile)
	daemon.logPrintStageStep(out, "install docker GPG key - %v %s", err, aptOut)
	// Add docker community edition repository
	lsbOut, err := platform.InvokeProgram(nil, misc.CommonOSCmdTimeoutSec, "lsb_release", "-cs")
	daemon.logPrintStageStep(out, "determine release name - %v %s", err, lsbOut)
	if err != nil {
		daemon.logPrintStageStep(out, "failed to determine release name")
		return
	}
	aptOut, err = platform.InvokeProgram(nil, misc.CommonOSCmdTimeoutSec, "add-apt-repository", fmt.Sprintf("https://download.docker.com/linux/debian %s stable", strings.TrimSpace(string(lsbOut))))
	daemon.logPrintStageStep(out, "enable docker repository - %v %s", err, aptOut)
}

func (daemon *Daemon) prepareDockerRepositoryForAWSLinux(out *bytes.Buffer) {
	if misc.HostIsWindows() {
		daemon.logPrintStageStep(out, "skipped on windows: prepare docker repository for AWS Linux system")
		return
	}
	daemon.logPrintStageStep(out, "prepare docker repository for AWS Linux system")
	content, err := ioutil.ReadFile("/etc/os-release")
	if err != nil {
		daemon.logPrintStageStep(out, "failed to read os-release, skip rest of the stage.")
		return
	} else if !strings.Contains(strings.ToLower(string(content)), "amazon") {
		daemon.logPrintStageStep(out, "system is not an Amazon Linux, skip rest of the stage.")
		return
	}
	installOut, err := platform.InvokeProgram(nil, misc.CommonOSCmdTimeoutSec, "/usr/bin/amazon-linux-extras", "install", "-y", "docker")
	if strings.Contains(installOut, "already installed") && err == nil {
		daemon.logPrintStageStep(out, "install docker via extras - ok")
	} else {
		daemon.logPrintStageStep(out, "install docker via extras - %v %s", err, installOut)
	}
}

/*
getSystemPackageManager returns executable path and name of package manager available on this system, as well as
environment variables and command arguments used to invoke them.
*/
func getSystemPackageManager() (pkgManagerPath, pkgManagerName string, pkgManagerEnv, pkgInstallArgs, sysUpgradeArgs []string) {
	if misc.HostIsWindows() {
		// Chocolatey is the only package manager supported on Windows
		pkgManagerPath = `C:\ProgramData\chocolatey\bin\choco.exe`
		pkgManagerName = "choco"
	} else {
		for _, binPrefix := range []string{"/sbin", "/bin", "/usr/sbin", "/usr/bin", "/usr/sbin/local", "/usr/bin/local"} {
			/*
				Prefer zypper over apt-get because opensuse has a weird "apt-get wrapper" that is not remotely functional.
				Prefer apt over apt-get because some public cloud OS templates can upgrade kernel via apt but not with apt-get.
			*/
			for _, execName := range []string{"yum", "zypper", "apt", "apt-get"} {
				pkgManagerPath = filepath.Join(binPrefix, execName)
				if _, err := os.Stat(pkgManagerPath); err == nil {
					pkgManagerName = execName
					break
				}
			}
			if pkgManagerName != "" {
				break
			}
		}
	}
	switch pkgManagerName {
	case "choco":
		// choco is simple and easy
		pkgInstallArgs = []string{"install", "-y"}
		sysUpgradeArgs = []string{"upgrade", "-y", "all"}
	case "yum":
		// yum is simple and easy
		pkgInstallArgs = []string{"-y", "-t", "--skip-broken", "install"}
		sysUpgradeArgs = []string{"-y", "-t", "--skip-broken", "update"}
	case "apt":
		// apt and apt-get are too old to be convenient
		fallthrough
	case "apt-get":
		pkgManagerEnv = []string{"DEBIAN_FRONTEND=noninteractive"}
		pkgInstallArgs = []string{"-q", "-y", "-f", "-m", "-o", "Dpkg::Options::=--force-confold", "-o", "Dpkg::Options::=--force-confdef", "install"}
		sysUpgradeArgs = []string{"-q", "-y", "-f", "-m", "-o", "Dpkg::Options::=--force-confold", "-o", "Dpkg::Options::=--force-confdef", "upgrade"}
	case "zypper":
		// zypper cannot English and consistency
		pkgInstallArgs = []string{"--non-interactive", "install", "--recommends", "--auto-agree-with-licenses", "--replacefiles", "--force-resolution"}
		sysUpgradeArgs = []string{"--non-interactive", "update", "--recommends", "--auto-agree-with-licenses", "--skip-interactive", "--replacefiles", "--force-resolution"}
	}
	return
}

/*
InstallSoftware uses system package manager to upgrade system software, and then install a laitos soft dependencies
along with additional software packages according to user configuration.
*/
func (daemon *Daemon) InstallSoftware(out *bytes.Buffer) {
	// Null input suppresses this action, empty input leads to only laitos recommendations to be installed.
	if daemon.InstallPackages == nil {
		return
	}

	// Prepare package manager
	if misc.HostIsWindows() {
		daemon.logPrintStageStep(out, "install windows features")
		shellOut, err := misc.InvokeShell(3600, misc.PowerShellInterpreterPath, `Install-WindowsFeature XPS-Viewer, WoW64-Support, Windows-TIFF-IFilter, PowerShell-ISE, Windows-Defender, Windows-Defender-Gui, TFTP-Client, Telnet-Client, Server-Media-Foundation, GPMC, NET-Framework-45-Core, WebDAV-Redirector`)
		if err != nil {
			daemon.logPrintStageStep(out, "failed to install windows features: %v - %s", err, shellOut)
		}
		daemon.logPrintStageStep(out, "install/upgrade chocolatey")
		shellOut, err = misc.InvokeShell(3600, misc.PowerShellInterpreterPath, `Set-ExecutionPolicy Bypass -Scope Process -Force; iex ((New-Object System.Net.WebClient).DownloadString('https://chocolatey.org/install.ps1'))`)
		if err != nil {
			daemon.logPrintStageStep(out, "failed to install/upgrade chocolatey: %v - %s", err, shellOut)
		}
	} else {
		daemon.prepareDockerRepositoryForDebian(out)
		daemon.prepareDockerRepositoryForAWSLinux(out)
	}

	daemon.logPrintStage(out, "upgrade system software")
	pkgManagerPath, pkgManagerName, pkgManagerEnv, pkgInstallArgs, sysUpgradeArgs := getSystemPackageManager()
	if pkgManagerName == "" {
		daemon.logPrintStageStep(out, "failed to find a compatible package manager")
		return
	}

	// apt-get is too old to be convenient
	if pkgManagerName == "apt-get" || pkgManagerName == "apt" {
		// Five minutes should be enough to grab the latest manifest
		result, err := platform.InvokeProgram(pkgManagerEnv, 5*60, pkgManagerPath, "update")
		// There is no need to suppress this output according to markers
		daemon.logPrintStageStep(out, "update apt manifests: %v - %s", err, strings.TrimSpace(result))
		// Fix interrupted package installation, otherwise no package will update/install in the next steps.
		result, err = platform.InvokeProgram(pkgManagerEnv, 2*3600, "dpkg", "--configure", "-a", "--force-confold", "--force-confdef")
		daemon.logPrintStageStep(out, "fix interrupted package installation: %v - %s", err, strings.TrimSpace(result))
	}

	// Upgrade system packages with a time constraint of two hours
	result, err := platform.InvokeProgram(pkgManagerEnv, 2*3600, pkgManagerPath, sysUpgradeArgs...)
	for _, marker := range suppressOutputMarkers {
		// If nothing was done during system update, suppress the rather useless output.
		if strings.Contains(strings.ToLower(result), marker) {
			result = "(nothing to do or upgrade not available)"
			break
		}
	}
	daemon.logPrintStageStep(out, "upgrade system result: %v - %s", err, strings.TrimSpace(result))

	/*
		Install additional software packages.
		laitos itself does not rely on any third-party library or program to run, however, it is very useful to install
		several PhantomJS/SlimerJS dependencies, as well as utility applications to help out with system diagnosis.
		Several of the packages are repeated under different names to accommodate the differences in naming convention
		among distributions.
	*/
	daemon.logPrintStage(out, "install software")
	pkgs := []string{
		// For outgoing HTTPS connections
		"ca-certificates",

		// Utilities for APT maintenance that also help with installer docker community edition on Debian
		"apt-transport-https", "gnupg", "software-properties-common",
		// Docker for running SlimerJS
		"docker", "docker-client", "docker.io", "docker-ce",

		// Soft and hard dependencies of PhantomJS
		"bzip2", "bzip2-libs", "cjkuni-fonts-common", "cjkuni-ukai-fonts", "cjkuni-uming-fonts", "dbus", "dejavu-fonts-common",
		"dejavu-sans-fonts", "dejavu-serif-fonts", "expat", "firefox", "font-noto", "fontconfig", "fontconfig-config",
		"fontpackages-filesystem", "fonts-arphic-ukai", "fonts-arphic-uming", "fonts-dejavu-core", "fonts-liberation", "freetype",
		"gnutls", "icu", "intlfonts-chinese-big-bitmap-fonts", "intlfonts-chinese-bitmap-fonts", "lib64z1", "libXfont", "libbz2-1",
		"libbz2-1.0", "liberation-fonts-common", "liberation-mono-fonts", "liberation-sans-fonts", "liberation-serif-fonts",
		"liberation2-fonts", "libexpat1", "libfontconfig1", "libfontenc", "libfreetype6", "libicu", "libicu57", "libicu60_2",
		"libpng", "libpng16-16", "nss", "openssl", "ttf-dejavu", "ttf-freefont", "ttf-liberation", "wqy-zenhei", "xfonts-utils",
		"xorg-x11-font-utils", "xorg-x11-fonts-Type1", "zlib", "zlib1g",

		// Time maintenance utilities
		"ntp", "ntpd", "ntpdate",

		// busybox and toybox are useful for general maintenance, and busybox can synchronise system clock as well.
		"busybox", "toybox",

		// Network diagnosis, system maintenance, and other useful utilities.
		"7zip", "bind-utils", "curl", "dateutils", "diffutils", "dnsutils", "finger", "hostname", "htop", "iftop", "iotop", "iputils-ping",
		"language-pack-en", "locales", "locales-all", "lftp", "lsof", "mailutils", "mailx", "moreutils", "nc", "net-snmp", "net-snmp-utils",
		"net-tools", "netcat", "nmap", "nmon", "patchutils", "perf", "procps", "psmisc", "rsync", "screen", "sensors", "snmp", "strace", "sudo",
		"tcpdump", "telnet", "tmux", "traceroute", "tree", "unar", "unzip", "usbutils", "util-linux-locales", "vim", "wget", "whois", "wiggle", "zip",
	}
	pkgs = append(pkgs, daemon.InstallPackages...)
	sort.Strings(pkgs)
	/*
		Although most package managers can install more than one packages at a time, the packages are still installed
		one after another, because:
		- apt-get does not ignore non-existent package names, how inconvenient.
		- if zypper runs into unsatisfactory package dependencies, it aborts the whole installation.
		yum is once again the superior solution among all three.
	*/
	for _, name := range pkgs {
		// Put software name next to installation parameters
		installCmd := make([]string, len(pkgInstallArgs)+1)
		copy(installCmd, pkgInstallArgs)
		installCmd[len(pkgInstallArgs)] = name
		// Ten minutes should be good enough for each package
		result, err := platform.InvokeProgram(pkgManagerEnv, 10*60, pkgManagerPath, installCmd...)
		if err == nil {
			daemon.logPrintStageStep(out, "install/upgrade %s: OK", name)
		} else {
			for _, marker := range suppressOutputMarkers {
				// If nothing was done about the package, suppress the rather useless output.
				if strings.Contains(strings.ToLower(result), marker) {
					result = "(nothing to do or not available)"
					break
				}
			}
			daemon.logPrintStageStep(out, "install/upgrade %s: %v - %s", name, err, strings.TrimSpace(result))
		}
	}
}
