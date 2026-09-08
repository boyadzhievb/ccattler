import { useState } from "react";
import siteData from "./site-data.json";

function Nav() {
  return (
    <nav className="fixed top-0 left-0 right-0 z-50 border-b border-white/5 backdrop-blur-md bg-[#080810]/80">
      <div className="max-w-6xl mx-auto px-6 h-14 flex items-center justify-between">
        <div className="flex items-center gap-2">
          <div className="w-6 h-6 border border-[#6378ff]/60 rounded-sm flex items-center justify-center">
            <div className="w-2.5 h-2.5 bg-[#6378ff] rounded-sm" />
          </div>
          <span className="font-mono text-sm font-semibold text-white tracking-tight">
            ccattler
          </span>
        </div>
        <div className="hidden md:flex items-center gap-8">
          {["Install", "Philosophy", "Architecture", "Capabilities", "Status", "Examples"].map((item) => (
            <a
              key={item}
              href={`#${item.toLowerCase()}`}
              className="text-sm text-white/45 hover:text-white/80 transition-colors duration-200 tracking-wide"
            >
              {item}
            </a>
          ))}
        </div>
        <div className="flex items-center gap-3">
          <a
            href="https://github.com/boyadzhievb/ccattler"
            className="font-mono text-xs text-white/40 hover:text-white/70 transition-colors flex items-center gap-1.5"
          >
            <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor">
              <path d="M12 0C5.37 0 0 5.37 0 12c0 5.31 3.435 9.795 8.205 11.385.6.105.825-.255.825-.57 0-.285-.015-1.23-.015-2.235-3.015.555-3.795-.735-4.035-1.41-.135-.345-.72-1.41-1.23-1.695-.42-.225-1.02-.78-.015-.795.945-.015 1.62.87 1.845 1.23 1.08 1.815 2.805 1.305 3.495.99.105-.78.42-1.305.765-1.605-2.67-.3-5.46-1.335-5.46-5.925 0-1.305.465-2.385 1.23-3.225-.12-.3-.54-1.53.12-3.18 0 0 1.005-.315 3.3 1.23.96-.27 1.98-.405 3-.405s2.04.135 3 .405c2.295-1.56 3.3-1.23 3.3-1.23.66 1.65.24 2.88.12 3.18.765.84 1.23 1.905 1.23 3.225 0 4.605-2.805 5.625-5.475 5.925.435.375.81 1.095.81 2.22 0 1.605-.015 2.895-.015 3.3 0 .315.225.69.825.57A12.02 12.02 0 0 0 24 12c0-6.63-5.37-12-12-12z" />
            </svg>
            GitHub
          </a>
          <a
            href="#install"
            className="font-mono text-xs bg-[#6378ff] hover:bg-[#7085ff] text-white px-3 py-1.5 rounded transition-colors duration-200"
          >
            Get Started
          </a>
        </div>
      </div>
    </nav>
  );
}

function FlowDiagram({ items, className = "" }: { items: { label: string; sub?: string }[]; className?: string }) {
  return (
    <div className={`flex flex-col items-center gap-0 ${className}`}>
      {items.map((item, i) => (
        <div key={i} className="flex flex-col items-center">
          <div className="relative group">
            <div className="border border-white/12 bg-white/[0.03] hover:bg-white/[0.06] hover:border-[#6378ff]/40 transition-all duration-300 px-5 py-2.5 rounded min-w-[160px] text-center">
              <div className="font-mono text-sm font-medium text-white/85">{item.label}</div>
              {item.sub && <div className="font-mono text-xs text-white/35 mt-0.5">{item.sub}</div>}
            </div>
            <div className="absolute inset-0 rounded opacity-0 group-hover:opacity-100 transition-opacity duration-300 pointer-events-none"
              style={{ boxShadow: "0 0 20px -4px rgba(99,120,255,0.35)" }} />
          </div>
          {i < items.length - 1 && (
            <div className="flex flex-col items-center py-1 gap-0.5">
              <div className="w-px h-3 bg-gradient-to-b from-white/20 to-white/8" />
              <svg width="8" height="6" viewBox="0 0 8 6" fill="none">
                <path d="M4 6L0 0h8L4 6z" fill="rgba(255,255,255,0.25)" />
              </svg>
            </div>
          )}
        </div>
      ))}
    </div>
  );
}

function Hero() {
  return (
    <section className="relative min-h-screen flex items-center pt-14 overflow-hidden">
      {/* Grid background */}
      <div className="absolute inset-0 grid-bg opacity-100" />
      {/* Radial glow */}
      <div className="absolute inset-0 pointer-events-none">
        <div
          className="absolute top-1/3 left-1/2 -translate-x-1/2 -translate-y-1/2 w-[700px] h-[500px] rounded-full animate-pulse-glow"
          style={{ background: "radial-gradient(ellipse, rgba(99,120,255,0.12) 0%, transparent 70%)" }}
        />
      </div>

      <div className="relative max-w-6xl mx-auto px-6 py-24 w-full">
        <div className="grid grid-cols-1 lg:grid-cols-[1fr_360px] gap-16 items-center">
          {/* Left */}
          <div>
            <div className="inline-flex items-center gap-2 border border-[#6378ff]/30 bg-[#6378ff]/5 rounded-full px-3.5 py-1.5 mb-8">
              <div className="w-1.5 h-1.5 rounded-full bg-[#6378ff] animate-pulse" />
              <span className="font-mono text-xs text-[#6378ff]/90 tracking-widest uppercase">
                Early access · Open source
              </span>
            </div>

            <h1 className="text-5xl md:text-6xl lg:text-7xl font-bold text-white leading-[1.05] tracking-tight mb-6 glow-text">
              Container<br />
              orchestration,<br />
              <span className="text-white/40">redesigned from</span><br />
              <span className="text-[#6378ff]">first principles.</span>
            </h1>

            <p className="text-lg text-white/45 leading-relaxed max-w-xl mb-10 font-light">
              CCattler is a declarative container management system built around{" "}
              <span className="text-white/70 font-mono text-base">facts</span>,{" "}
              <span className="text-white/70 font-mono text-base">relations</span>,{" "}
              <span className="text-white/70 font-mono text-base">desired state</span>,{" "}
              <span className="text-white/70 font-mono text-base">observed state</span>,{" "}
              <span className="text-white/70 font-mono text-base">constraints</span>,{" "}
              <span className="text-white/70 font-mono text-base">policies</span>, and{" "}
              <span className="text-white/70 font-mono text-base">reconciliation</span>{" "}
              — without making objects and YAML the foundation.
            </p>

            <div className="flex items-center gap-4">
              <a
                href="#install"
                className="font-mono text-sm bg-[#6378ff] hover:bg-[#7085ff] text-white px-6 py-3 rounded transition-all duration-200 hover:shadow-[0_0_24px_-4px_rgba(99,120,255,0.6)]"
              >
                Get Started →
              </a>
              <a
                href="https://github.com/boyadzhievb/ccattler"
                className="font-mono text-sm border border-white/15 hover:border-white/30 text-white/60 hover:text-white/90 px-6 py-3 rounded transition-all duration-200 flex items-center gap-2"
              >
                <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor" className="opacity-70">
                  <path d="M12 0C5.37 0 0 5.37 0 12c0 5.31 3.435 9.795 8.205 11.385.6.105.825-.255.825-.57 0-.285-.015-1.23-.015-2.235-3.015.555-3.795-.735-4.035-1.41-.135-.345-.72-1.41-1.23-1.695-.42-.225-1.02-.78-.015-.795.945-.015 1.62.87 1.845 1.23 1.08 1.815 2.805 1.305 3.495.99.105-.78.42-1.305.765-1.605-2.67-.3-5.46-1.335-5.46-5.925 0-1.305.465-2.385 1.23-3.225-.12-.3-.54-1.53.12-3.18 0 0 1.005-.315 3.3 1.23.96-.27 1.98-.405 3-.405s2.04.135 3 .405c2.295-1.56 3.3-1.23 3.3-1.23.66 1.65.24 2.88.12 3.18.765.84 1.23 1.905 1.23 3.225 0 4.605-2.805 5.625-5.475 5.925.435.375.81 1.095.81 2.22 0 1.605-.015 2.895-.015 3.3 0 .315.225.69.825.57A12.02 12.02 0 0 0 24 12c0-6.63-5.37-12-12-12z" />
                </svg>
                GitHub
              </a>
            </div>
          </div>

          {/* Right: flow diagram */}
          <div className="flex justify-center lg:justify-end">
            <div className="relative">
              <div className="absolute inset-0 -m-6 rounded-2xl"
                style={{ background: "radial-gradient(ellipse at center, rgba(99,120,255,0.06) 0%, transparent 75%)" }} />
              <FlowDiagram
                items={[
                  { label: "Intent" },
                  { label: "Facts" },
                  { label: "Policies + Constraints" },
                  { label: "Desired State" },
                  { label: "Reconciliation" },
                  { label: "Containers" },
                  { label: "Observations", sub: "→ State" },
                ]}
              />
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}

function Installation() {
  const [copied, setCopied] = useState<string | null>(null);

  const copyToClipboard = (text: string, id: string) => {
    navigator.clipboard.writeText(text);
    setCopied(id);
    setTimeout(() => setCopied(null), 2000);
  };

  return (
    <section id="install" className="relative py-32 border-t border-white/5">
      <div className="absolute inset-0 grid-bg opacity-30" />
      <div className="relative max-w-6xl mx-auto px-6">
        <div className="mb-16">
          <div className="font-mono text-xs text-[#6378ff]/70 tracking-widest uppercase mb-4">§ 00 · Install</div>
          <h2 className="text-4xl md:text-5xl font-bold text-white leading-tight tracking-tight max-w-2xl">
            One command.
            <span className="text-white/35"> Ready in seconds.</span>
          </h2>
        </div>

        <div className="grid grid-cols-1 lg:grid-cols-2 gap-8">
          {/* Quick install */}
          <div className="border border-[#6378ff]/25 rounded-lg p-8 bg-[#6378ff]/[0.03] glow-blue">
            <div className="flex items-center gap-2.5 mb-6">
              <div className="w-2 h-2 rounded-full bg-[#6378ff]" />
              <span className="font-mono text-xs text-[#6378ff]/80 tracking-widest uppercase">Quick install</span>
            </div>

            <div className="border border-white/8 rounded-lg overflow-hidden mb-6">
              <div className="border-b border-white/6 bg-white/[0.025] px-4 py-2.5 flex items-center justify-between">
                <div className="flex items-center gap-2">
                  <div className="w-2 h-2 rounded-full bg-white/10" />
                  <div className="w-2 h-2 rounded-full bg-white/10" />
                  <div className="w-2 h-2 rounded-full bg-white/10" />
                </div>
                <button
                  onClick={() => copyToClipboard("curl -fsSL https://raw.githubusercontent.com/boyadzhievb/ccattler/master/scripts/install.sh | sh", "curl")}
                  className="font-mono text-[10px] text-white/30 hover:text-white/60 transition-colors"
                >
                  {copied === "curl" ? "copied!" : "copy"}
                </button>
              </div>
              <div className="p-5 bg-[#04040c] font-mono text-sm">
                <div>
                  <span className="text-[#6378ff]/60">$</span>
                  <span className="text-white/60"> curl -fsSL https://raw.githubusercontent.com/</span>
                </div>
                <div>
                  <span className="text-white/60">  boyadzhievb/ccattler/master/scripts/install.sh | sh</span>
                </div>
              </div>
            </div>

            <p className="text-sm text-white/35 leading-relaxed">
              Detects your OS and architecture automatically. Installs the <code className="text-white/50">cca</code> binary to <code className="text-white/50">/usr/local/bin</code>.
              Supports macOS and Linux (amd64 / arm64).
            </p>
          </div>

          {/* Alternative methods */}
          <div className="space-y-4">
            <div className="border border-white/8 rounded-lg p-6 bg-white/[0.015]">
              <div className="flex items-center gap-2.5 mb-4">
                <div className="w-2 h-2 rounded-full bg-white/20" />
                <span className="font-mono text-xs text-white/35 tracking-widest uppercase">From source</span>
              </div>
              <div className="border border-white/6 rounded bg-[#04040c] p-4 font-mono text-sm space-y-1">
                <div><span className="text-[#6378ff]/60">$</span><span className="text-white/60"> git clone https://github.com/boyadzhievb/ccattler</span></div>
                <div><span className="text-[#6378ff]/60">$</span><span className="text-white/60"> cd ccattler</span></div>
                <div><span className="text-[#6378ff]/60">$</span><span className="text-white/60"> go build -o cca ./cmd/cca/</span></div>
              </div>
              <p className="text-xs text-white/25 mt-3">Requires Go 1.22+</p>
            </div>

            <div className="border border-white/8 rounded-lg p-6 bg-white/[0.015]">
              <div className="flex items-center gap-2.5 mb-4">
                <div className="w-2 h-2 rounded-full bg-white/20" />
                <span className="font-mono text-xs text-white/35 tracking-widest uppercase">GitHub Releases</span>
              </div>
              <p className="text-sm text-white/40 leading-relaxed mb-3">
                Pre-built binaries for every tagged release.
              </p>
              <a
                href="https://github.com/boyadzhievb/ccattler/releases"
                className="font-mono text-xs text-[#6378ff]/70 hover:text-[#6378ff] transition-colors flex items-center gap-1.5"
              >
                github.com/boyadzhievb/ccattler/releases →
              </a>
            </div>

            <div className="border border-white/8 rounded-lg p-6 bg-white/[0.015]">
              <div className="flex items-center gap-2.5 mb-4">
                <div className="w-2 h-2 rounded-full bg-white/20" />
                <span className="font-mono text-xs text-white/35 tracking-widest uppercase">Verify</span>
              </div>
              <div className="border border-white/6 rounded bg-[#04040c] p-4 font-mono text-sm space-y-1">
                <div><span className="text-[#6378ff]/60">$</span><span className="text-white/60"> cca version</span></div>
                <div><span className="text-[#a3e8a0]/60">cca v0.5.5</span></div>
                <div className="pt-2"><span className="text-[#6378ff]/60">$</span><span className="text-white/60"> cca demo</span></div>
                <div><span className="text-[#a3e8a0]/60">Applying config...</span></div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}

function Philosophy() {
  return (
    <section id="philosophy" className="relative py-32 border-t border-white/5">
      <div className="absolute inset-0 grid-bg opacity-40" />
      <div className="relative max-w-6xl mx-auto px-6">
        <div className="mb-16">
          <div className="font-mono text-xs text-[#6378ff]/70 tracking-widest uppercase mb-4">§ 01 · Philosophy</div>
          <h2 className="text-4xl md:text-5xl font-bold text-white leading-tight tracking-tight max-w-2xl">
            Stop modeling the system as objects.
            <span className="text-white/35"> Model the system as state.</span>
          </h2>
        </div>

        <div className="grid grid-cols-1 lg:grid-cols-2 gap-8 items-start">
          {/* Traditional */}
          <div className="border border-white/8 rounded-lg p-8 bg-white/[0.015]">
            <div className="flex items-center gap-2.5 mb-6">
              <div className="w-2 h-2 rounded-full bg-white/20" />
              <span className="font-mono text-xs text-white/35 tracking-widest uppercase">Traditional</span>
            </div>
            <div className="space-y-3">
              {[
                { label: "Objects", desc: "Resources as mutable entities" },
                { label: "API", desc: "Imperative mutation operations" },
                { label: "YAML", desc: "Configuration-as-truth" },
                { label: "Controllers", desc: "Tangled per-resource reconcilers" },
              ].map((item, i, arr) => (
                <div key={i} className="flex flex-col items-start gap-0">
                  <div className="border border-white/8 bg-white/[0.02] px-4 py-2.5 rounded w-full">
                    <div className="flex items-center justify-between">
                      <span className="font-mono text-sm text-white/60">{item.label}</span>
                      <span className="text-xs text-white/25">{item.desc}</span>
                    </div>
                  </div>
                  {i < arr.length - 1 && (
                    <div className="flex flex-col items-center w-full py-1">
                      <div className="flex items-center gap-2 text-white/20">
                        <div className="flex-1 h-px bg-white/8" />
                        <span className="font-mono text-xs">→</span>
                        <div className="flex-1 h-px bg-white/8" />
                      </div>
                    </div>
                  )}
                </div>
              ))}
            </div>
            <div className="mt-6 pt-6 border-t border-white/5">
              <p className="text-sm text-white/25 font-light leading-relaxed">
                Controllers fight each other. State drifts. You write more YAML to fix your YAML.
              </p>
            </div>
          </div>

          {/* CCattler */}
          <div className="border border-[#6378ff]/25 rounded-lg p-8 bg-[#6378ff]/[0.03] glow-blue">
            <div className="flex items-center gap-2.5 mb-6">
              <div className="w-2 h-2 rounded-full bg-[#6378ff]" />
              <span className="font-mono text-xs text-[#6378ff]/80 tracking-widest uppercase">CCattler</span>
            </div>
            <div className="space-y-3">
              {[
                { label: "Facts", desc: "Immutable, accumulated truth" },
                { label: "Relations", desc: "Explicit dependency graph" },
                { label: "Constraints", desc: "Policies as first-class data" },
                { label: "State", desc: "Single unified world-view" },
                { label: "Reconciliation", desc: "One loop, all capabilities" },
              ].map((item, i, arr) => (
                <div key={i} className="flex flex-col items-start gap-0">
                  <div className="border border-[#6378ff]/20 bg-[#6378ff]/[0.04] px-4 py-2.5 rounded w-full hover:border-[#6378ff]/40 transition-colors duration-200">
                    <div className="flex items-center justify-between">
                      <span className="font-mono text-sm text-white/80">{item.label}</span>
                      <span className="text-xs text-white/35">{item.desc}</span>
                    </div>
                  </div>
                  {i < arr.length - 1 && (
                    <div className="flex flex-col items-center w-full py-1">
                      <div className="flex items-center gap-2 text-[#6378ff]/40">
                        <div className="flex-1 h-px bg-[#6378ff]/15" />
                        <span className="font-mono text-xs">↓</span>
                        <div className="flex-1 h-px bg-[#6378ff]/15" />
                      </div>
                    </div>
                  )}
                </div>
              ))}
            </div>
            <div className="mt-6 pt-6 border-t border-[#6378ff]/10">
              <p className="text-sm text-white/45 font-light leading-relaxed">
                The system converges. Drift is impossible by construction. You describe what you want, not how to get there.
              </p>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}

function ArchNode({
  label,
  sub,
  highlight,
  wide,
}: {
  label: string;
  sub?: string;
  highlight?: boolean;
  wide?: boolean;
}) {
  return (
    <div
      className={`
        border rounded px-4 py-3 text-center transition-all duration-300 hover:scale-[1.02] cursor-default
        ${highlight
          ? "border-[#6378ff]/50 bg-[#6378ff]/8 hover:border-[#6378ff]/70"
          : "border-white/10 bg-white/[0.025] hover:border-white/25"
        }
        ${wide ? "min-w-[220px]" : "min-w-[140px]"}
      `}
      style={highlight ? { boxShadow: "0 0 24px -6px rgba(99,120,255,0.3)" } : {}}
    >
      <div className={`font-mono text-sm font-medium ${highlight ? "text-white" : "text-white/70"}`}>{label}</div>
      {sub && <div className="font-mono text-xs text-white/30 mt-0.5">{sub}</div>}
    </div>
  );
}

function ArchArrow({ label }: { label?: string }) {
  return (
    <div className="flex flex-col items-center py-1 gap-0.5">
      {label && <span className="font-mono text-[10px] text-white/20 mb-0.5">{label}</span>}
      <div className="w-px h-4 bg-gradient-to-b from-white/20 to-white/5" />
      <svg width="8" height="5" viewBox="0 0 8 5" fill="none">
        <path d="M4 5L0 0h8L4 5z" fill="rgba(255,255,255,0.2)" />
      </svg>
    </div>
  );
}

function Architecture() {
  return (
    <section id="architecture" className="relative py-32 border-t border-white/5">
      <div className="absolute inset-0 grid-bg opacity-30" />
      <div className="absolute inset-0 pointer-events-none">
        <div
          className="absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 w-[900px] h-[600px] rounded-full"
          style={{ background: "radial-gradient(ellipse, rgba(99,120,255,0.05) 0%, transparent 65%)" }}
        />
      </div>

      <div className="relative max-w-6xl mx-auto px-6">
        <div className="mb-16">
          <div className="font-mono text-xs text-[#6378ff]/70 tracking-widest uppercase mb-4">§ 02 · Architecture</div>
          <h2 className="text-4xl md:text-5xl font-bold text-white leading-tight tracking-tight max-w-2xl">
            One control loop.
            <span className="text-white/35"> Many capabilities.</span>
          </h2>
          <p className="text-white/40 text-lg mt-4 max-w-xl font-light">
            Every capability — scheduling, scaling, networking, storage — runs through a single, principled reconciliation engine.
          </p>
        </div>

        {/* Large architecture diagram */}
        <div className="border border-white/6 rounded-xl p-8 md:p-12 bg-white/[0.01] relative overflow-hidden">
          <div className="absolute top-4 right-4 font-mono text-xs text-white/15 tracking-widest">
            CONTROL PLANE OVERVIEW
          </div>

          <div className="flex flex-col items-center gap-0">
            {/* Entry */}
            <div className="flex items-center gap-4">
              <ArchNode label="CLI" sub="cca apply" />
              <div className="font-mono text-white/20 text-xl">·</div>
              <ArchNode label="API" sub="HTTP / gRPC" />
            </div>
            <ArchArrow />

            <ArchNode label="Intent" highlight wide />
            <ArchArrow label="parse & validate" />

            <ArchNode label="Distributed State" sub="facts · relations · history" highlight wide />
            <ArchArrow />

            {/* Parallel layer */}
            <div className="flex items-center gap-3 flex-wrap justify-center">
              <ArchNode label="Scheduler" sub="placement" />
              <div className="font-mono text-white/15 text-sm px-1">·</div>
              <ArchNode label="Autoscaler" sub="H / V / event" />
              <div className="font-mono text-white/15 text-sm px-1">·</div>
              <ArchNode label="Policy Engine" sub="RBAC · ABAC" />
            </div>
            <ArchArrow label="compute target state" />

            <ArchNode label="Desired State" highlight wide />
            <ArchArrow label="diff observed → desired" />

            <ArchNode label="Reconciliation Loop" highlight wide />
            <ArchArrow label="actuate" />

            {/* Runtime layer */}
            <div className="flex items-center gap-3 flex-wrap justify-center">
              <ArchNode label="Runtime" sub="OCI · containerd" />
              <div className="font-mono text-white/15 text-sm px-1">·</div>
              <ArchNode label="Network" sub="CNI · mTLS" />
              <div className="font-mono text-white/15 text-sm px-1">·</div>
              <ArchNode label="Storage" sub="CSI · volumes" />
            </div>
            <ArchArrow />

            <ArchNode label="Containers" highlight wide />
            <ArchArrow label="emit observations" />

            {/* Feedback loop */}
            <div className="flex items-center gap-6 flex-wrap justify-center">
              <ArchNode label="Observations" sub="metrics · events" />
              <div className="flex flex-col items-center gap-1">
                <div className="font-mono text-xs text-white/25">→</div>
                <div className="font-mono text-[10px] text-white/15">feed back</div>
              </div>
              <ArchNode label="Observed State" sub="reconciler input" />
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}

const capabilities = [
  {
    title: "Declarative — no YAML",
    desc: "Define intent in a purpose-built configuration language. No templating hacks, no helm charts, no kustomize overlays.",
    tag: "config",
  },
  {
    title: "Desired vs observed state",
    desc: "The system continuously compares what you declared with what it observes. Drift is corrected automatically.",
    tag: "core",
  },
  {
    title: "Autoscaling — H, V, event",
    desc: "Horizontal, vertical, and event-driven scaling as first-class capabilities. Defined inline with your service declaration.",
    tag: "scaling",
  },
  {
    title: "Pluggable networking / CNI",
    desc: "Swap network implementations without changing your service definitions. Policy and topology are separate concerns.",
    tag: "network",
  },
  {
    title: "Identity, mTLS, RBAC + ABAC",
    desc: "Workload identity is automatic. mTLS everywhere by default. Authorization policies are facts, not middleware.",
    tag: "security",
  },
  {
    title: "Multi-tenancy, quotas, isolation",
    desc: "Namespaces and quotas as hard constraints. Resource isolation enforced at the state layer, not controller layer.",
    tag: "isolation",
  },
  {
    title: "Distributed state",
    desc: "State is replicated, consistent, and auditable. No single point of failure. Every fact is timestamped and attributed.",
    tag: "infra",
  },
  {
    title: "Local laptop development",
    desc: "Full fidelity local environment. Run the complete control plane on your machine with a single binary.",
    tag: "dx",
  },
];

const configExample = `service checkout {
    image "example/checkout:1.4"
    instances 3

    resources {
        cpu    500m
        memory 1Gi
    }

    scale horizontally {
        min 2
        max 20
        cpu 60%
    }
}`;

function Capabilities() {
  const [activeTag, setActiveTag] = useState<string | null>(null);
  const tags = Array.from(new Set(capabilities.map((c) => c.tag)));

  const filtered = activeTag ? capabilities.filter((c) => c.tag === activeTag) : capabilities;

  return (
    <section id="capabilities" className="relative py-32 border-t border-white/5">
      <div className="absolute inset-0 grid-bg opacity-25" />
      <div className="relative max-w-6xl mx-auto px-6">
        <div className="mb-12">
          <div className="font-mono text-xs text-[#6378ff]/70 tracking-widest uppercase mb-4">§ 03 · Capabilities</div>
          <h2 className="text-4xl md:text-5xl font-bold text-white leading-tight tracking-tight max-w-2xl">
            Everything you need.
            <span className="text-white/35"> Nothing you don't.</span>
          </h2>
        </div>

        <div className="grid grid-cols-1 lg:grid-cols-[1fr_380px] gap-12 items-start">
          {/* Capabilities grid */}
          <div>
            <div className="flex flex-wrap gap-2 mb-8">
              <button
                onClick={() => setActiveTag(null)}
                className={`font-mono text-xs px-3 py-1.5 rounded border transition-all duration-200 ${
                  activeTag === null
                    ? "border-[#6378ff]/50 bg-[#6378ff]/10 text-[#6378ff]/90"
                    : "border-white/10 text-white/35 hover:text-white/60 hover:border-white/20"
                }`}
              >
                all
              </button>
              {tags.map((tag) => (
                <button
                  key={tag}
                  onClick={() => setActiveTag(activeTag === tag ? null : tag)}
                  className={`font-mono text-xs px-3 py-1.5 rounded border transition-all duration-200 ${
                    activeTag === tag
                      ? "border-[#6378ff]/50 bg-[#6378ff]/10 text-[#6378ff]/90"
                      : "border-white/10 text-white/35 hover:text-white/60 hover:border-white/20"
                  }`}
                >
                  {tag}
                </button>
              ))}
            </div>

            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              {filtered.map((cap) => (
                <div
                  key={cap.title}
                  className="border border-white/8 bg-white/[0.02] rounded-lg p-5 hover:border-white/18 hover:bg-white/[0.035] transition-all duration-200 group"
                >
                  <div className="flex items-start justify-between gap-3 mb-2">
                    <h3 className="font-medium text-white/85 text-sm leading-snug">{cap.title}</h3>
                    <span className="font-mono text-[10px] text-white/20 border border-white/8 rounded px-1.5 py-0.5 shrink-0">
                      {cap.tag}
                    </span>
                  </div>
                  <p className="text-sm text-white/35 leading-relaxed font-light">{cap.desc}</p>
                </div>
              ))}
            </div>
          </div>

          {/* Config example */}
          <div className="sticky top-24">
            <div className="border border-white/8 rounded-lg overflow-hidden">
              <div className="border-b border-white/6 bg-white/[0.025] px-4 py-3 flex items-center justify-between">
                <div className="flex items-center gap-2">
                  <div className="w-2.5 h-2.5 rounded-full bg-white/10" />
                  <div className="w-2.5 h-2.5 rounded-full bg-white/10" />
                  <div className="w-2.5 h-2.5 rounded-full bg-white/10" />
                </div>
                <span className="font-mono text-xs text-white/25">checkout.ccattler</span>
                <span className="font-mono text-[10px] text-[#6378ff]/50 border border-[#6378ff]/20 rounded px-2 py-0.5">
                  ccl
                </span>
              </div>
              <pre className="font-mono text-sm leading-7 p-6 bg-[#06060e] overflow-x-auto">
                {configExample.split("\n").map((line, i) => {
                  const indent = line.match(/^(\s+)/)?.[1]?.length ?? 0;
                  const trimmed = line.trimStart();

                  const keyword = /^(service|image|instances|resources|cpu|memory|scale|min|max)\b/.test(trimmed);
                  const num = /^[\d.]+[a-zA-Z%]*$/.test(trimmed.split(" ")[1] ?? "");
                  const string = /"[^"]*"/.test(trimmed);
                  const brace = /^[{}]$/.test(trimmed);
                  const directive = /^(horizontally|vertically)\b/.test(trimmed);

                  return (
                    <div key={i} className="whitespace-pre">
                      <span className="text-transparent select-none">{line.slice(0, indent)}</span>
                      {brace ? (
                        <span className="text-white/25">{trimmed}</span>
                      ) : keyword ? (
                        <>
                          <span className="text-[#6378ff]/80">{trimmed.split(" ")[0]}</span>
                          {trimmed.includes('"') ? (
                            <span className="text-[#a3e8a0]"> {trimmed.slice(trimmed.indexOf('"'))}</span>
                          ) : directive ? (
                            <span className="text-white/50"> {trimmed.split(" ").slice(1).join(" ")}</span>
                          ) : (
                            <span className="text-white/60"> {trimmed.split(" ").slice(1).join(" ")}</span>
                          )}
                        </>
                      ) : (
                        <span className="text-white/35">{trimmed}</span>
                      )}
                    </div>
                  );
                })}
              </pre>
            </div>

            <div className="mt-4 grid grid-cols-3 gap-2">
              {[
                { label: "3", desc: "instances" },
                { label: "500m", desc: "cpu" },
                { label: "2–20", desc: "scale range" },
              ].map((stat) => (
                <div key={stat.label} className="border border-white/6 rounded p-3 text-center bg-white/[0.015]">
                  <div className="font-mono text-base font-semibold text-white/80">{stat.label}</div>
                  <div className="font-mono text-[10px] text-white/25 mt-0.5">{stat.desc}</div>
                </div>
              ))}
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}

function ProjectStatus() {
  const completedCount = siteData.milestones.filter((m) => m.complete).length;
  const totalCount = siteData.milestones.length;

  return (
    <section id="status" className="relative py-32 border-t border-white/5">
      <div className="absolute inset-0 grid-bg opacity-25" />
      <div className="relative max-w-6xl mx-auto px-6">
        <div className="mb-12">
          <div className="font-mono text-xs text-[#6378ff]/70 tracking-widest uppercase mb-4">§ 04 · Project status</div>
          <h2 className="text-4xl md:text-5xl font-bold text-white leading-tight tracking-tight max-w-2xl">
            Built in the open.
            <span className="text-white/35"> Tested relentlessly.</span>
          </h2>
        </div>

        {/* Stats row */}
        <div className="grid grid-cols-2 md:grid-cols-4 gap-4 mb-12">
          {[
            { value: String(siteData.testCount), label: "tests passing" },
            { value: String(siteData.packageCount), label: "packages" },
            { value: `${completedCount}/${totalCount}`, label: "milestones" },
            { value: String(siteData.demoCommands.length), label: "demo commands" },
          ].map((stat) => (
            <div key={stat.label} className="border border-white/8 rounded-lg p-5 bg-white/[0.02] text-center">
              <div className="font-mono text-2xl font-bold text-[#6378ff]">{stat.value}</div>
              <div className="font-mono text-xs text-white/35 mt-1">{stat.label}</div>
            </div>
          ))}
        </div>

        {/* Milestones */}
        <div className="grid grid-cols-1 lg:grid-cols-[1fr_340px] gap-10 items-start">
          <div>
            <h3 className="font-mono text-sm text-white/60 mb-6 tracking-wide">Milestones</h3>
            <div className="space-y-2">
              {siteData.milestones.map((m) => (
                <div
                  key={m.id}
                  className={`border rounded-lg px-5 py-3 flex items-center gap-4 transition-all duration-200 ${
                    m.complete
                      ? "border-[#6378ff]/30 bg-[#6378ff]/[0.04]"
                      : "border-white/6 bg-white/[0.015]"
                  }`}
                >
                  <div className={`w-5 h-5 rounded-full border-2 flex items-center justify-center shrink-0 ${
                    m.complete ? "border-[#6378ff] bg-[#6378ff]/20" : "border-white/15"
                  }`}>
                    {m.complete && (
                      <svg width="10" height="8" viewBox="0 0 10 8" fill="none">
                        <path d="M1 4l2.5 2.5L9 1" stroke="#6378ff" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
                      </svg>
                    )}
                  </div>
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <span className={`font-mono text-xs ${m.complete ? "text-[#6378ff]/80" : "text-white/30"}`}>{m.id}</span>
                      <span className={`text-sm font-medium ${m.complete ? "text-white/80" : "text-white/40"}`}>{m.name}</span>
                    </div>
                    <div className={`text-xs mt-0.5 ${m.complete ? "text-white/35" : "text-white/20"}`}>{m.demo}</div>
                  </div>
                  <span className={`font-mono text-[10px] shrink-0 ${m.complete ? "text-[#a3e8a0]/70" : "text-white/15"}`}>
                    {m.complete ? "COMPLETE" : m.phase}
                  </span>
                </div>
              ))}
            </div>

            {/* Progress bar */}
            <div className="mt-6">
              <div className="flex items-center justify-between mb-2">
                <span className="font-mono text-xs text-white/30">Progress</span>
                <span className="font-mono text-xs text-[#6378ff]/70">{Math.round((completedCount / totalCount) * 100)}%</span>
              </div>
              <div className="h-1.5 bg-white/5 rounded-full overflow-hidden">
                <div
                  className="h-full bg-gradient-to-r from-[#6378ff] to-[#6378ff]/60 rounded-full transition-all duration-500"
                  style={{ width: `${(completedCount / totalCount) * 100}%` }}
                />
              </div>
            </div>
          </div>

          {/* Demo commands */}
          <div className="sticky top-24">
            <h3 className="font-mono text-sm text-white/60 mb-6 tracking-wide">Try it</h3>
            <div className="border border-white/8 rounded-lg overflow-hidden">
              <div className="border-b border-white/6 bg-white/[0.025] px-4 py-3 flex items-center gap-2">
                <div className="w-2 h-2 rounded-full bg-white/10" />
                <div className="w-2 h-2 rounded-full bg-white/10" />
                <div className="w-2 h-2 rounded-full bg-white/10" />
                <span className="font-mono text-xs text-white/20 ml-2">terminal</span>
              </div>
              <div className="p-4 bg-[#06060e] space-y-4">
                {siteData.demoCommands.map((cmd) => (
                  <div key={cmd.command}>
                    <div className="flex items-center gap-2">
                      <span className="text-[#6378ff]/60 font-mono text-sm">$</span>
                      <span className="text-white/70 font-mono text-sm">{cmd.command}</span>
                    </div>
                    <div className="text-white/25 font-mono text-xs ml-5 mt-0.5">{cmd.description}</div>
                  </div>
                ))}
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}

function Examples() {
  const [selectedExample, setSelectedExample] = useState(0);
  const [showDsl, setShowDsl] = useState(false);

  const filteredExamples = siteData.examples.filter((e) => e.description);

  const activeExample = filteredExamples[selectedExample];

  return (
    <section id="examples" className="relative py-32 border-t border-white/5">
      <div className="absolute inset-0 grid-bg opacity-20" />
      <div className="relative max-w-6xl mx-auto px-6">
        <div className="mb-12">
          <div className="font-mono text-xs text-[#6378ff]/70 tracking-widest uppercase mb-4">§ 05 · Examples</div>
          <h2 className="text-4xl md:text-5xl font-bold text-white leading-tight tracking-tight max-w-2xl">
            Real configs.
            <span className="text-white/35"> Not contrived demos.</span>
          </h2>
        </div>

        {/* Tabs: Examples / DSL Reference */}
        <div className="flex gap-2 mb-8">
          <button
            onClick={() => setShowDsl(false)}
            className={`font-mono text-xs px-4 py-2 rounded border transition-all duration-200 ${
              !showDsl
                ? "border-[#6378ff]/50 bg-[#6378ff]/10 text-[#6378ff]/90"
                : "border-white/10 text-white/35 hover:text-white/60 hover:border-white/20"
            }`}
          >
            Example Configs
          </button>
          <button
            onClick={() => setShowDsl(true)}
            className={`font-mono text-xs px-4 py-2 rounded border transition-all duration-200 ${
              showDsl
                ? "border-[#6378ff]/50 bg-[#6378ff]/10 text-[#6378ff]/90"
                : "border-white/10 text-white/35 hover:text-white/60 hover:border-white/20"
            }`}
          >
            DSL Reference
          </button>
        </div>

        {!showDsl ? (
          <div className="grid grid-cols-1 lg:grid-cols-[220px_1fr] gap-6">
            {/* Example list */}
            <div className="flex lg:flex-col gap-2 overflow-x-auto lg:overflow-visible pb-2 lg:pb-0">
              {filteredExamples.map((ex, i) => (
                <button
                  key={ex.name}
                  onClick={() => setSelectedExample(i)}
                  className={`text-left px-4 py-3 rounded-lg border transition-all duration-200 shrink-0 ${
                    selectedExample === i
                      ? "border-[#6378ff]/40 bg-[#6378ff]/[0.06]"
                      : "border-white/6 bg-white/[0.015] hover:border-white/15"
                  }`}
                >
                  <div className={`font-mono text-sm ${selectedExample === i ? "text-white/85" : "text-white/50"}`}>
                    {ex.name}
                  </div>
                  <div className={`text-xs mt-0.5 truncate ${selectedExample === i ? "text-white/40" : "text-white/20"}`}>
                    {ex.filename}
                  </div>
                </button>
              ))}
            </div>

            {/* Code display */}
            {activeExample && (
              <div className="border border-white/8 rounded-lg overflow-hidden">
                <div className="border-b border-white/6 bg-white/[0.025] px-4 py-3 flex items-center justify-between">
                  <div className="flex items-center gap-2">
                    <div className="w-2.5 h-2.5 rounded-full bg-white/10" />
                    <div className="w-2.5 h-2.5 rounded-full bg-white/10" />
                    <div className="w-2.5 h-2.5 rounded-full bg-white/10" />
                  </div>
                  <span className="font-mono text-xs text-white/25">{activeExample.filename}</span>
                  <span className="font-mono text-[10px] text-[#6378ff]/50 border border-[#6378ff]/20 rounded px-2 py-0.5">
                    ccl
                  </span>
                </div>
                <pre className="font-mono text-sm leading-7 p-6 bg-[#06060e] overflow-x-auto max-h-[500px] overflow-y-auto">
                  {activeExample.content.split("\n").map((line, i) => {
                    const trimmed = line.trimStart();
                    const indent = line.length - trimmed.length;

                    const isComment = trimmed.startsWith("#");
                    const keyword = /^(service|image|instances|resources|cpu|memory|expose|health|http|tcp|every|volume|size|persistent)\b/.test(trimmed);
                    const brace = /^[{}]$/.test(trimmed);
                    const hasString = /"[^"]*"/.test(trimmed);

                    return (
                      <div key={i} className="whitespace-pre">
                        <span className="text-transparent select-none">{line.slice(0, indent)}</span>
                        {isComment ? (
                          <span className="text-white/20">{trimmed}</span>
                        ) : brace ? (
                          <span className="text-white/25">{trimmed}</span>
                        ) : keyword ? (
                          <>
                            <span className="text-[#6378ff]/80">{trimmed.split(" ")[0]}</span>
                            {hasString ? (
                              <span className="text-[#a3e8a0]"> {trimmed.slice(trimmed.indexOf('"'))}</span>
                            ) : (
                              <span className="text-white/60"> {trimmed.split(" ").slice(1).join(" ")}</span>
                            )}
                          </>
                        ) : (
                          <span className="text-white/45">{trimmed}</span>
                        )}
                      </div>
                    );
                  })}
                </pre>
                {activeExample.description && (
                  <div className="border-t border-white/6 bg-white/[0.015] px-6 py-3">
                    <span className="text-xs text-white/35">{activeExample.description}</span>
                  </div>
                )}
              </div>
            )}
          </div>
        ) : (
          /* DSL Reference table */
          <div className="border border-white/8 rounded-lg overflow-hidden">
            <div className="border-b border-white/6 bg-white/[0.025] px-5 py-3">
              <span className="font-mono text-xs text-white/40 tracking-wide">DSL Keywords</span>
            </div>
            <div className="divide-y divide-white/5">
              {siteData.dslFeatures.map((feat, i) => (
                <div key={i} className="px-5 py-3.5 flex items-start gap-4 hover:bg-white/[0.02] transition-colors">
                  <code className="font-mono text-sm text-[#6378ff]/80 shrink-0 w-24">{feat.keyword}</code>
                  <span className="font-mono text-[10px] text-white/25 border border-white/8 rounded px-2 py-0.5 shrink-0">
                    {feat.context}
                  </span>
                  <span className="text-sm text-white/45 leading-relaxed">{feat.description}</span>
                </div>
              ))}
            </div>
          </div>
        )}
      </div>
    </section>
  );
}

function CTA() {
  return (
    <section className="relative py-36 border-t border-white/5">
      <div className="absolute inset-0 grid-bg opacity-20" />
      <div className="absolute inset-0 pointer-events-none">
        <div
          className="absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 w-[600px] h-[400px] rounded-full animate-pulse-glow"
          style={{ background: "radial-gradient(ellipse, rgba(99,120,255,0.1) 0%, transparent 65%)" }}
        />
      </div>

      <div className="relative max-w-4xl mx-auto px-6 text-center">
        <div className="font-mono text-xs text-[#6378ff]/70 tracking-widest uppercase mb-8">§ 06 · Get started</div>

        <h2 className="text-5xl md:text-6xl font-bold text-white leading-tight tracking-tight mb-6">
          Build the container platform
          <br />
          <span className="text-white/35">you actually want.</span>
        </h2>

        <p className="text-lg text-white/40 leading-relaxed max-w-xl mx-auto mb-12 font-light">
          CCattler is an experiment in rethinking container orchestration from the ground up. Join the early access program and help shape where it goes.
        </p>

        <div className="flex items-center justify-center gap-4 flex-wrap mb-16">
          <a
            href="#install"
            className="font-mono text-sm bg-[#6378ff] hover:bg-[#7085ff] text-white px-8 py-3.5 rounded transition-all duration-200 hover:shadow-[0_0_32px_-4px_rgba(99,120,255,0.5)]"
          >
            Get Started →
          </a>
          <a
            href="https://github.com/boyadzhievb/ccattler"
            className="font-mono text-sm border border-white/15 hover:border-white/30 text-white/60 hover:text-white/90 px-8 py-3.5 rounded transition-all duration-200 flex items-center gap-2"
          >
            <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor" className="opacity-70">
              <path d="M12 0C5.37 0 0 5.37 0 12c0 5.31 3.435 9.795 8.205 11.385.6.105.825-.255.825-.57 0-.285-.015-1.23-.015-2.235-3.015.555-3.795-.735-4.035-1.41-.135-.345-.72-1.41-1.23-1.695-.42-.225-1.02-.78-.015-.795.945-.015 1.62.87 1.845 1.23 1.08 1.815 2.805 1.305 3.495.99.105-.78.42-1.305.765-1.605-2.67-.3-5.46-1.335-5.46-5.925 0-1.305.465-2.385 1.23-3.225-.12-.3-.54-1.53.12-3.18 0 0 1.005-.315 3.3 1.23.96-.27 1.98-.405 3-.405s2.04.135 3 .405c2.295-1.56 3.3-1.23 3.3-1.23.66 1.65.24 2.88.12 3.18.765.84 1.23 1.905 1.23 3.225 0 4.605-2.805 5.625-5.475 5.925.435.375.81 1.095.81 2.22 0 1.605-.015 2.895-.015 3.3 0 .315.225.69.825.57A12.02 12.02 0 0 0 24 12c0-6.63-5.37-12-12-12z" />
            </svg>
            GitHub
          </a>
          <a
            href="#architecture"
            className="font-mono text-sm border border-white/8 hover:border-white/20 text-white/35 hover:text-white/60 px-8 py-3.5 rounded transition-all duration-200"
          >
            Architecture ↗
          </a>
        </div>

        {/* Terminal snippet */}
        <div className="border border-white/8 rounded-lg overflow-hidden max-w-lg mx-auto text-left">
          <div className="border-b border-white/6 bg-white/[0.02] px-4 py-2.5 flex items-center gap-2">
            <div className="w-2 h-2 rounded-full bg-white/10" />
            <div className="w-2 h-2 rounded-full bg-white/10" />
            <div className="w-2 h-2 rounded-full bg-white/10" />
            <span className="font-mono text-xs text-white/20 ml-2">terminal</span>
          </div>
          <div className="p-5 bg-[#04040c] font-mono text-sm space-y-2">
            <div>
              <span className="text-[#6378ff]/60">$</span>
              <span className="text-white/60"> curl -fsSL install.ccattler.dev | sh</span>
            </div>
            <div className="text-white/25 text-xs">Installing cca v0.5.5...</div>
            <div className="text-[#a3e8a0]/70 text-xs">✓ cca installed to /usr/local/bin</div>
            <div className="mt-3">
              <span className="text-[#6378ff]/60">$</span>
              <span className="text-white/60"> cca init &amp;&amp; cca up</span>
            </div>
            <div className="text-white/25 text-xs">Starting control plane...</div>
            <div className="text-[#a3e8a0]/70 text-xs">✓ Reconciliation loop active</div>
          </div>
        </div>
      </div>

      {/* Footer */}
      <div className="relative max-w-6xl mx-auto px-6 mt-24 pt-8 border-t border-white/5">
        <div className="flex items-center justify-between flex-wrap gap-4">
          <div className="flex items-center gap-2">
            <div className="w-5 h-5 border border-[#6378ff]/40 rounded-sm flex items-center justify-center">
              <div className="w-2 h-2 bg-[#6378ff] rounded-sm" />
            </div>
            <span className="font-mono text-sm text-white/50 font-semibold">CCattler</span>
            <span className="font-mono text-xs text-white/20">— Container Cattler</span>
          </div>
          <div className="flex items-center gap-6 font-mono text-xs text-white/20">
            <span>Open source</span>
            <span>·</span>
            <span>Built from first principles</span>
            <span>·</span>
            <span>Apache 2.0</span>
          </div>
        </div>
      </div>
    </section>
  );
}

export default function App() {
  return (
    <div className="min-h-screen bg-[#080810] text-white">
      <Nav />
      <Hero />
      <Installation />
      <Philosophy />
      <Architecture />
      <Capabilities />
      <ProjectStatus />
      <Examples />
      <CTA />
    </div>
  );
}
