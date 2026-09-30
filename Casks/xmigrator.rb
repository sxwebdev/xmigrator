# Bootstrapped from published v0.1.0 archives; GoReleaser updates this on release.
cask "xmigrator" do
  define_singleton_method(:xmigrator_postflight_steps) do
    postflight_steps do
      on_macos do
        run "/usr/bin/xattr",
            args: ["-d", "-r", "com.apple.quarantine", "{{staged_path}}/xmigrator"]
      end
    end
  end

  xmigrator_postflight_steps

  version "0.1.0"

  on_macos do
    on_arm do
      sha256 "4ac2bb9325d4b8c1b19d2a0aae10239b68b47b6b9ca7ec19469f7b4d9431e1f6"
      url "https://github.com/sxwebdev/xmigrator/releases/download/v#{version}/xmigrator_#{version}_darwin_arm64.tar.gz"
    end
    on_intel do
      sha256 "7039cd617c88d682fd886a413f8f3136cd9fda2b5d3d2fbc5083b00350ececab"
      url "https://github.com/sxwebdev/xmigrator/releases/download/v#{version}/xmigrator_#{version}_darwin_amd64.tar.gz"
    end
  end
  on_linux do
    on_arm do
      sha256 "b3103c5cea1d0d685cae2d77a50d3e8d2bf0cecc5f8dad1f262a8cd5f4feb828"
      url "https://github.com/sxwebdev/xmigrator/releases/download/v#{version}/xmigrator_#{version}_linux_arm64.tar.gz"
    end
    on_intel do
      sha256 "ff4237dd1818db2a4c421ce6ad3681dffd388a797c1b5c6f8959e80c5a189ebe"
      url "https://github.com/sxwebdev/xmigrator/releases/download/v#{version}/xmigrator_#{version}_linux_amd64.tar.gz"
    end
  end

  name "xmigrator"
  desc "Transactional SQL migrations for PostgreSQL and SQLite"
  homepage "https://github.com/sxwebdev/xmigrator"

  livecheck do
    skip "Auto-generated on release."
  end

  binary "xmigrator"

  # No zap stanza required
end
