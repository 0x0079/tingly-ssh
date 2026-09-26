// Command snippets shared by both languages. Keep them in sync with the
// Quick start in README.md / README.zh-CN.md.

export const REPO = "https://github.com/0x0079/tingly-ssh";
export const DOCS = `${REPO}/tree/main/docs`;
export const blob = (path: string) => `${REPO}/blob/main/${path}`;

export const code = {
  install: `# VERSION, OS (linux|darwin), ARCH (amd64|arm64)
curl -LO ${REPO}/releases/download/vVERSION/tingly-ssh_VERSION_OS_ARCH.tar.gz
tar xzf tingly-ssh_VERSION_OS_ARCH.tar.gz
sudo install -m 755 tingly-ssh /usr/local/bin/`,
  goInstall: "go install github.com/0x0079/tingly-ssh/cmd/tingly-ssh@latest",
  server: `sudo mkdir -p /etc/tingly
sudo sh -c 'cat ~alice/.ssh/authorized_keys >> /etc/tingly/authorized_keys'

tingly-ssh server --listen :7443 --target 127.0.0.1:22 \\
    --authorized-keys /etc/tingly/authorized_keys`,
  client: `Host myserver-roam
    HostName 203.0.113.10
    User alice
    ProxyCommand tingly-ssh proxy --server %h:7443`,
  tryIt: "ssh myserver-roam 'while true; do date; sleep 1; done'",
};
