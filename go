#! /usr/bin/env ruby

require 'English'

Dir.chdir File.dirname(__FILE__)

def try_command_and_restart(command)
  exit $CHILD_STATUS.exitstatus unless system command
  exec({ 'RUBYOPT' => nil }, RbConfig.ruby, *[$PROGRAM_NAME].concat(ARGV))
end

begin
  require 'bundler/setup' if File.exist? 'Gemfile'
rescue LoadError
  try_command_and_restart 'gem install bundler'
rescue SystemExit
  try_command_and_restart 'bundle install'
end

begin
  require 'go_script'
rescue LoadError
  try_command_and_restart 'gem install go_script' unless File.exist? 'Gemfile'
  abort "Please add \"gem 'go_script'\" to your Gemfile"
end

extend GoScript
check_ruby_version '2.3.0'

command_group :build, 'Image and container building commands'

LOCAL_ROOT_DIR = File.absolute_path(File.dirname(__FILE__))
APP_SYS_ROOT = '/usr/local/18f'
NETWORK = '18f/knowledge-sharing-toolkit'

IMAGES = %w(
  dev-base
  dev-standard
  nginx
  oauth2_proxy
  hmacproxy
  authdelegate
  pages
  lunr-server
  team-api
)

DATA_CONTAINERS = {
  'pages-data' => 'pages',
  'team-api-data' => 'team-api',
}

DAEMONS = {
  'lunr-server' => {
    data_containers: ['pages-data:ro'],
  },
  'nginx' => {
    flags: '-p 80:80 -p 443:443',
    data_containers: [
      'pages-data:ro',
      'team-api-data:ro',
    ],
  },
  'pages' => {
    data_containers: ['pages-data:rw']
  },
  'oauth2_proxy' => {
    data_containers: [],
  },
  'hmacproxy' => {
    data_containers: [],
  },
  'authdelegate' => {
    data_containers: [],
  },
  'team-api' => {
    data_containers: ['team-api-data:rw'],
  },
}

NEEDS_SSH = %w(team-api)

def _check_names(names, collection, type_label)
  names.each do |name|
    next if collection.include?(name)
    puts "\"#{name}\" does not match any known #{type_label}; " \
      "valid #{type_label}s are:\n  #{collection.join("\n  ")}"
    exit 1
  end
  names
end

def _images(args)
  args.empty? ? IMAGES : _check_names(args, IMAGES, 'image')
end

def _data_containers(args)
  known_containers = DATA_CONTAINERS.keys
  return known_containers if args.empty?
  _check_names(args, known_containers, 'data container')
end

def _daemons(args)
  daemons = DAEMONS.keys
  args.empty? ? daemons : _check_names(args, daemons, 'daemon')
end

def_command :build_images, 'Build Docker images' do |args|
  _images(args).each do |image|
    message = "Building #{image}"
    marker = '-' * message.size
    puts "#{marker}\n#{message}\n#{marker}"
    exec_cmd "docker build -t #{image} -f ./#{image}/Dockerfile ./#{image}"
  end
end

def_command :create_data_containers, 'Create Docker data containers' do |args|
  _data_containers(args).each do |container_name|
    base_image = DATA_CONTAINERS[container_name]
    exec_cmd "if ! $(docker ps -a | grep -q ' #{container_name}$'); then " \
      "docker run --name #{container_name} #{base_image} " \
      "echo Created data container \\\"#{container_name}\\\" " \
      "from \\\"#{base_image}\\\"; fi"
  end
end

command_group :run_containers, 'Container running commands'

def _network_is_running
  `docker network ls`.split("\n")[1..-1]
    .map { |network| network.gsub(/  */, ' ').split[1] }
    .include?(NETWORK)
end

def_command :create_network, 'Start the local network between containers' do
  return if _network_is_running
  exec_cmd "docker network create --driver bridge #{NETWORK}"
end

def_command :rm_network, 'Start the local network between containers' do
  exec_cmd "docker network rm #{NETWORK}" if _network_is_running
end

def _config_dir_volume_binding(image_name)
  local_config_dir = File.join(LOCAL_ROOT_DIR, image_name, 'config')
  image_config_dir = "#{APP_SYS_ROOT}/#{image_name}/config"
  "-v #{local_config_dir}:#{image_config_dir}:ro"
end

def _ssh_config_dir_volume_binding(image_name)
  NEEDS_SSH.include?(image_name) ? _config_dir_volume_binding('ssh') : ''
end

def _volumes_from(data_containers)
  data_containers.map { |container| "--volumes-from #{container}" }.join(' ')
end

def _run_container(image_name, options, command: '', data_containers: [])
  puts "Running: #{image_name}"

  # Remove any existing containers matching the image name.
  exec_cmd "if $(docker ps -a | grep -q ' #{image_name}$'); then " \
    "docker rm #{image_name}; fi"

  # Mount the corresponding config directories as volumes. Name the container
  # the same as the image.
  exec_cmd "docker run #{options} --name #{image_name} " \
    "#{_config_dir_volume_binding(image_name)} " \
    "#{_ssh_config_dir_volume_binding(image_name)} " \
    "#{_network_is_running ? "--net=#{NETWORK}" : '' } " \
    "#{_volumes_from(data_containers)} #{image_name} #{command}"
end

def_command :run_daemons, 'Run Docker containers as daemons' do |args|
  _daemons(args).each do |daemon_name|
    daemon = DAEMONS[daemon_name]
    _run_container(daemon_name, "-d #{daemon[:flags]}",
      data_containers: daemon[:data_containers])
  end
end

def_command :run_container, 'Run a shell within a Docker container' do |args|
  if args.empty?
    puts 'run_container accepts a container name and an argument list'
  end
  image = args.shift
  _images([image])
  command = args.empty? ? '/bin/bash' : args.join(' ')
  _run_container(image, '-it', command: command,
    data_containers: DAEMONS[image][:data_containers])
end

def_command :reload_nginx, 'Reload Nginx after a config change' do
  exec_cmd 'docker kill -s HUP nginx'
end

def_command :stop_daemons, 'Stop Docker containers running as daemons' do |args|
  _daemons(args).each do |daemon|
    exec_cmd "if $(docker ps -a | grep -q ' #{daemon}$'); then " \
      "docker stop #{daemon}; fi"
  end
end

command_group :cleanup, 'Image and container cleanup commands'

def_command :rm_containers, 'Remove stopped (non-data) containers' do |args|
  images = _images(args)
  containers = `docker ps -a`.split("\n")[1..-1]
    .map { |container| container.match(/ ([^ ]*)$/)[1] }
    .reject { |container| container.end_with?('-data') }
    .select { |container| images.include?(container) }
  exec_cmd "docker rm #{containers.join(' ')}" unless containers.empty?
end

def_command :rm_images, 'Remove unused images' do
  unused_images = `docker images`.split("\n")[1..-1]
    .select { |image| image.start_with?('<none>') }
    .map { |image| image.gsub(/  */, ' ').split(' ')[2] }
  exec_cmd "docker rmi #{unused_images.join(' ')}" unless unused_images.empty?
end

execute_command ARGV
