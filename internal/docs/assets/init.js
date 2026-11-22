const apiBasePath = window.location.pathname.replace(/\/docs\/?$/, "/") || "/";

Scalar.createApiReference("#app", {
  url: "openapi.json",
  servers: [{ url: apiBasePath, description: "Same origin" }],
  layout: "modern",
  theme: "default",
  darkMode: window.matchMedia("(prefers-color-scheme: dark)").matches,
  hideDarkModeToggle: false,
  hideSearch: false,
  hideTestRequestButton: false,
  showOperationId: false,
  showDeveloperTools: "never",
  withDefaultFonts: false,
  persistAuth: false,
  telemetry: false,
  agent: { disabled: true },
});
