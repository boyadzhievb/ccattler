import{_ as a,o as n,c as t,a2 as i}from"./chunks/framework.D66Xq0Kv.js";const k=JSON.parse('{"title":"","description":"","frontmatter":{"layout":"home","hero":{"name":"CCattler","text":"Container orchestration, redesigned.","tagline":"A declarative container management system built on facts, relations, desired state, and reconciliation — not objects and YAML.","actions":[{"theme":"brand","text":"Get Started","link":"/getting-started"},{"theme":"alt","text":"GitHub","link":"https://github.com/boyadzhievb/ccattler"}]},"features":[{"title":"Facts, not objects","details":"The cluster stores facts and relations — not mutable API objects. No Pods, no ReplicaSets, no Deployments. Just desired state, observed state, and the gap between them."},{"title":"No YAML","details":"A purpose-built declarative language for expressing intent. No apiVersion/kind boilerplate, no templating hacks, no helm charts."},{"title":"One reconciliation loop","details":"Scheduling, scaling, networking, storage — every capability runs through a single principled engine. Controllers communicate only through state."},{"title":"Zero-trust security","details":"mTLS everywhere, internal CA with auto-rotation, RBAC + ABAC, per-controller least privilege, identity-based network policies."},{"title":"Unified autoscaling","details":"Horizontal, vertical, event-driven, and scheduled scaling through one mechanism. No separate HPA/VPA/KEDA systems."},{"title":"Multi-tenancy built in","details":"Tenant ownership, resource quotas, fair scheduling, and identity-based isolation from day one — not bolted on."}]},"headers":[],"relativePath":"index.md","filePath":"index.md"}'),e={name:"index.md"};function l(p,s,r,h,o,c){return n(),t("div",null,[...s[0]||(s[0]=[i(`<h2 id="how-it-works" tabindex="-1">How it works <a class="header-anchor" href="#how-it-works" aria-label="Permalink to &quot;How it works&quot;">​</a></h2><div class="language- vp-adaptive-theme"><button title="Copy Code" class="copy"></button><span class="lang"></span><pre class="shiki shiki-themes github-light github-dark vp-code" tabindex="0"><code><span class="line"><span>        USER</span></span>
<span class="line"><span>          |</span></span>
<span class="line"><span>          v</span></span>
<span class="line"><span>  +---------------+</span></span>
<span class="line"><span>  | DOMAIN LANG.  |    .ccattler files</span></span>
<span class="line"><span>  +-------+-------+</span></span>
<span class="line"><span>          |</span></span>
<span class="line"><span>          v</span></span>
<span class="line"><span>  +---------------+</span></span>
<span class="line"><span>  |  FACT STORE   |    etcd</span></span>
<span class="line"><span>  +-------+-------+</span></span>
<span class="line"><span>          |</span></span>
<span class="line"><span>  +-------+--------+</span></span>
<span class="line"><span>  |       |        |</span></span>
<span class="line"><span>  v       v        v</span></span>
<span class="line"><span>sched  network  storage    (controllers watch facts, derive actions)</span></span>
<span class="line"><span>  |       |        |</span></span>
<span class="line"><span>  +-------+--------+</span></span>
<span class="line"><span>          |</span></span>
<span class="line"><span>          v</span></span>
<span class="line"><span>  +---------------+</span></span>
<span class="line"><span>  | NODE AGENTS   |    one per machine, runs containers</span></span>
<span class="line"><span>  +-------+-------+</span></span>
<span class="line"><span>          |</span></span>
<span class="line"><span>          v</span></span>
<span class="line"><span>      MACHINES</span></span></code></pre></div><p>The cluster stores <strong>facts</strong> (desired state, observed state, constraints), not objects. Controllers are rule engines that watch facts, compare desired vs actual, and derive actions. The reconciliation loop runs continuously:</p><div class="language- vp-adaptive-theme"><button title="Copy Code" class="copy"></button><span class="lang"></span><pre class="shiki shiki-themes github-light github-dark vp-code" tabindex="0"><code><span class="line"><span>facts → rules → new facts → actions → reality → observations → facts</span></span></code></pre></div><h2 id="ccattler-vs-kubernetes" tabindex="-1">CCattler vs Kubernetes <a class="header-anchor" href="#ccattler-vs-kubernetes" aria-label="Permalink to &quot;CCattler vs Kubernetes&quot;">​</a></h2><table tabindex="0"><thead><tr><th></th><th>Kubernetes</th><th>CCattler</th></tr></thead><tbody><tr><td><strong>State model</strong></td><td>API objects (Deployment, ReplicaSet, Pod...)</td><td>Facts and relations</td></tr><tr><td><strong>Config format</strong></td><td>YAML serializing internal objects</td><td>Domain language expressing intent</td></tr><tr><td><strong>Extensibility</strong></td><td>CRDs + custom controllers</td><td>Typed facts + schemas + rules</td></tr><tr><td><strong>Networking</strong></td><td>Label selectors</td><td>Identity-based policies</td></tr><tr><td><strong>Multi-tenancy</strong></td><td>Namespaces (one concept for everything)</td><td>Separate ownership, quotas, isolation</td></tr><tr><td><strong>Autoscaling</strong></td><td>HPA/VPA/KEDA (separate systems)</td><td>Unified scaling engine</td></tr><tr><td><strong>Security</strong></td><td>RBAC on API resources</td><td>RBAC + ABAC on fact prefixes</td></tr></tbody></table><h2 id="configuration-language" tabindex="-1">Configuration language <a class="header-anchor" href="#configuration-language" aria-label="Permalink to &quot;Configuration language&quot;">​</a></h2><div class="language-hcl vp-adaptive-theme"><button title="Copy Code" class="copy"></button><span class="lang">hcl</span><pre class="shiki shiki-themes github-light github-dark vp-code" tabindex="0"><code><span class="line"><span style="--shiki-light:#6F42C1;--shiki-dark:#B392F0;">service</span><span style="--shiki-light:#005CC5;--shiki-dark:#79B8FF;"> web</span><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;"> {</span></span>
<span class="line"><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;">    image nginx</span><span style="--shiki-light:#D73A49;--shiki-dark:#F97583;">:</span><span style="--shiki-light:#005CC5;--shiki-dark:#79B8FF;">1.28</span></span>
<span class="line"><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;">    instances </span><span style="--shiki-light:#005CC5;--shiki-dark:#79B8FF;">3</span></span>
<span class="line"><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;">    expose </span><span style="--shiki-light:#005CC5;--shiki-dark:#79B8FF;">8080</span></span>
<span class="line"></span>
<span class="line"><span style="--shiki-light:#6F42C1;--shiki-dark:#B392F0;">    health</span><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;"> {</span></span>
<span class="line"><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;">        http </span><span style="--shiki-light:#D73A49;--shiki-dark:#F97583;">/</span><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;">health</span></span>
<span class="line"><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;">        every 10s</span></span>
<span class="line"><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;">    }</span></span>
<span class="line"></span>
<span class="line"><span style="--shiki-light:#6F42C1;--shiki-dark:#B392F0;">    resources</span><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;"> {</span></span>
<span class="line"><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;">        cpu 500m</span></span>
<span class="line"><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;">        memory 512Mi</span></span>
<span class="line"><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;">    }</span></span>
<span class="line"></span>
<span class="line"><span style="--shiki-light:#6F42C1;--shiki-dark:#B392F0;">    scale</span><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;"> {</span></span>
<span class="line"><span style="--shiki-light:#6F42C1;--shiki-dark:#B392F0;">        horizontal</span><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;"> {</span></span>
<span class="line"><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;">            min </span><span style="--shiki-light:#005CC5;--shiki-dark:#79B8FF;">3</span></span>
<span class="line"><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;">            max </span><span style="--shiki-light:#005CC5;--shiki-dark:#79B8FF;">30</span></span>
<span class="line"><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;">            target cpu</span><span style="--shiki-light:#D73A49;--shiki-dark:#F97583;"> =</span><span style="--shiki-light:#005CC5;--shiki-dark:#79B8FF;"> 60</span><span style="--shiki-light:#D73A49;--shiki-dark:#F97583;">%</span></span>
<span class="line"><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;">        }</span></span>
<span class="line"><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;">    }</span></span>
<span class="line"></span>
<span class="line"><span style="--shiki-light:#6F42C1;--shiki-dark:#B392F0;">    placement</span><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;"> {</span></span>
<span class="line"><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;">        architecture amd64</span></span>
<span class="line"><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;">        zone spread</span></span>
<span class="line"><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;">    }</span></span>
<span class="line"><span style="--shiki-light:#24292E;--shiki-dark:#E1E4E8;">}</span></span></code></pre></div>`,8)])])}const g=a(e,[["render",l]]);export{k as __pageData,g as default};
