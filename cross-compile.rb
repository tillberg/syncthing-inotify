#!/usr/bin/env ruby
# If you get the error "go build runtime: <runtime> must be bootstrapped using make.bash", please run:
# `/bin/bash -ic 'cd $(dirname $(dirname $(which go)))/src && ./make.bash'`

oses = {
  "darwin" => ["386", "amd64"],
  "dragonfly" => ["386", "amd64"],
  "freebsd" => ["386", "amd64", "arm"],
  "linux" => ["386", "amd64", "arm"],
  "netbsd" => ["386", "amd64"],
  "openbsd" => ["386", "amd64"],
  "solaris" => ["amd64"],
  "windows" => ["386", "amd64"]}

version = `git describe --abbrev=0 --tags`.chomp

2.times do
  bootstrapped = false
  oses.each do |os, archs|
    next if ARGV.count > 0 && !ARGV.include?(os)
    archs.each do |arch|
      puts "building #{os}-#{arch}"
      name = "syncthing-inotify"
      newname = "syncthing-inotify-#{os}-#{arch}"
      cross = os.gsub(/-v\d/,"")
      if os.include?("windows")
        name = name + ".exe"
      end
      vars = "GOOS=#{os} GOARCH=#{arch}"
      build = "#{vars} go build 2>&1"
      package = "tar -czf syncthing-inotify-#{os}-#{arch}-#{version}.tar.gz #{name}"
      rename = "mv #{name} #{newname}"
      output = `#{build} && #{package} && #{rename}`
      puts output unless output.empty?
      if output.include?("must be bootstrapped")
        `cd $(dirname $(which go))/../src && #{vars} ./make.bash --no-clean 1>&2`
        bootstrapped = true
      end
    end
  end
  exit unless bootstrapped
end
