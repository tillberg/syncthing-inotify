#### What is this?
Syncthing ([core](https://github.com/syncthing/syncthing)) uses a rescan interval to detect changes in folders. This application (syncthing-inotify) uses OS primitives to detect changes as soon as they happen. Therefore, if you save a file, syncthing-inotify will know about it and pass this information to Syncthing such that near real-time synchronisation can be achieved.

#### Where do I get it?
Syncthing-inotify binaries are released on [github](https://github.com/syncthing/syncthing-inotify/releases/latest)

#### How do I run it?
Syncthing-inotify will automatically read the Syncthing config if it is found in the standard path.
  * Run and hide on unix in a screen
```
screen -S inotify -dm ./syncthing-inotify
```
  * Run and hide on windows in .vbs script
```
CreateObject("Wscript.Shell").Run "syncthing-inotify.exe, 0, True
```
  * Run and hide on windows using API key in .vbs script
```
CreateObject("Wscript.Shell").Run "syncthing-inotify.exe -api=""...""", 0, True
```
  * Install as a service, see the etc/ folder

#### I'm confused
  * Try [Syncthing-GTK](https://github.com/syncthing/syncthing-gtk)
  * Read the commandline options: ```./syncthing-inotify -help```. Settings, such as an API key, need to be manually provided if you use a custom home for Syncthing.

#### Building syncthing-inotify
```
# To clone
mkdir -p src/github.com/syncthing
git clone https://github.com/syncthing/syncthing-inotify.git src/github.com/syncthing/syncthing-inotify
# Following commands are needed every time you want to build (unless you use Golang's specific folder structure: C:\src  or ~/src/)
export GOPATH=$(pwd)
cd src/github.com/syncthing/syncthing-inotify
go get
go build
```


#### Troubleshooting for folders with many files on Linux
* Linux limits the amount of inotify watchers (typically to [8192](http://stackoverflow.com/a/20355253)). Therefore, if you wish to sync many files and folders, you'll need to increase the upper limit:

  Permanently fix `Too many open files` for Linux: ```sudo sh -c 'echo fs.inotify.max_user_watches=204800\n >> /etc/sysctl.conf'```
  
  Fix `Too many open files` for Linux until next reboot: ```sudo sh -c 'echo fs.inotify.max_user_watches=204800\n >> /etc/sysctl.conf'``` (should be applied before launching syncthing-inotify)
