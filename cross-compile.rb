#!/usr/bin/env ruby
# If you get the error "go build runtime: <runtime> must be bootstrapped using make.bash", please run:
# `/bin/bash -ic 'cd $(dirname $(dirname $(which go)))/src && ./make.bash'`

oses = [
  "linux-386", "linux-amd64", "linux-arm",
  "darwin-386", "darwin-amd64",
  "openbsd-386", "openbsd-amd64",
  "freebsd-386", "freebsd-amd64", "freebsd-arm",
  "windows-386", "windows-amd64"]

version = `git describe --abbrev=0 --tags`.chomp

oses.each do |os|
  next if ARGV.count > 0 && !ARGV.include?(os)
  puts "building #{os}"
  name = "syncthing-inotify"
  newname = "syncthing-inotify-#{os}"
  buildX = ""
  cross = os.gsub(/-v\d/,"")
  if os.include?("windows")
    name = name + ".exe"
  end
  build = "#{buildX} go-#{cross} build"
  package = "tar -czf syncthing-inotify-#{os}-#{version}.tar.gz #{name}"
  rename = "mv #{name} #{newname}"
  `/bin/bash -ic 'source ../../davecheney/golang-crosscompile/crosscompile.bash && #{build} && #{package} && #{rename}'`
end
