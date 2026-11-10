import { defineConfig } from "astro/config";
import starlight from "@astrojs/starlight";
import starlightLinksValidator from "starlight-links-validator";
import blog from "starlight-blog";

// Published to GitHub Pages as a project site. Both values are required for
// canonical URLs and the blog RSS feed under the /TriageLab/ base path.
export default defineConfig({
  site: "https://bondrenai.github.io",
  base: "/TriageLab",
  integrations: [
    starlight({
      title: "TriageLab",
      description:
        "A Kubernetes-based tool that automates technical investigations for tickets and issues — routing, classification, and sandboxed investigation with recorded evidence.",
      sidebar: [
        {
          label: "Start",
          items: [
            { label: "What is TriageLab", slug: "" },
            { label: "Quickstart", slug: "start/quickstart" },
            { label: "The example investigation", slug: "start/example" },
          ],
        },
        {
          label: "Guides",
          items: [
            { label: "Browser workspace", slug: "guides/workspace" },
            { label: "Pause and resume", slug: "guides/pause-resume" },
            { label: "Sign-in and roles", slug: "guides/sign-in" },
            { label: "API clients (OAuth)", slug: "guides/api-clients" },
            { label: "Defining agents", slug: "guides/agents" },
            { label: "Zendesk tickets", slug: "guides/tickets-zendesk" },
            { label: "GitHub issues", slug: "guides/tickets-github" },
            { label: "Linear issues", slug: "guides/tickets-linear" },
            { label: "Helm deployment", slug: "guides/helm" },
            { label: "Telemetry", slug: "guides/telemetry" },
          ],
        },
        {
          label: "Reference",
          items: [
            { label: "CLI", slug: "reference/cli" },
            { label: "Configuration", slug: "reference/configuration" },
            { label: "Configuration examples", slug: "reference/configuration-examples" },
            { label: "Reports", slug: "reference/reports" },
            { label: "Upstream compatibility", slug: "reference/upstream" },
            { label: "Architecture", slug: "reference/architecture" },
          ],
        },
        {
          label: "Articles",
          items: [
            { label: "Evidence, not answers", slug: "articles/evidence-not-answers" },
            { label: "Pinning alpha upstreams", slug: "articles/pinning-alpha-upstreams" },
          ],
        },
      ],
      customCss: ["./src/styles/global.css"],
      plugins: [
        starlightLinksValidator(),
        blog({
          title: "Blog",
          description:
            "Release notes and working notes from building an evidence-driven investigator.",
        }),
      ],
    }),
  ],
});
