import "server-only";

// OIDC configuration resolved from server-only environment. These mirror the
// backend's Keycloak defaults (issuer on :8081, the public web client) so the
// frontend and backend validate the same realm without separate wiring. The
// issuer and client id are public OIDC identifiers, not secrets; the public
// client uses PKCE and carries no client secret.

// DEFAULT_ISSUER and DEFAULT_CLIENT_ID match the backend's local-dev Keycloak
// realm so the dev stack works with no extra environment.
const DEFAULT_ISSUER = "http://localhost:8081/realms/truth-in-stream";
const DEFAULT_CLIENT_ID = "truth-in-stream-web";
const DEFAULT_APP_URL = "http://localhost:3000";

// OidcConfig is the resolved set of values every auth route handler needs.
// issuer is the public identity tokens carry and the browser is redirected to;
// internalUrl is where this server runs OIDC discovery and the back-channel
// token exchanges from. They differ only in a split-horizon deployment (the
// docker dev stack: browser reaches Keycloak at localhost:8081, this container
// reaches it at keycloak:8081); everywhere else internalUrl === issuer.
export interface OidcConfig {
  issuer: string;
  internalUrl: string;
  clientId: string;
  appUrl: string;
  redirectUri: string;
  postLogoutRedirectUri: string;
}

// AuthEnv is the subset of environment variables the OIDC config reads. A narrow
// record (not NodeJS.ProcessEnv) keeps the function injectable in tests without
// having to satisfy the full process-env type.
type AuthEnv = Record<string, string | undefined>;

// readOidcConfig resolves the OIDC config from the given environment, defaulting
// to the local-dev realm. The redirect and post-logout URIs are derived from the
// app URL so they always match the registered Keycloak client (which allows
// http://localhost:3000/*). Exported with an injectable env for testing; the
// default reads process.env.
export function readOidcConfig(env: AuthEnv = process.env): OidcConfig {
  const issuer = (env.KEYCLOAK_ISSUER ?? DEFAULT_ISSUER).replace(/\/$/, "");
  // KEYCLOAK_INTERNAL_URL is the base this server uses for discovery and the
  // back-channel token/refresh calls when the issuer host is not reachable from
  // inside the container (the docker dev stack). It defaults to the issuer, so an
  // unset value is the ordinary single-host topology with no behaviour change.
  const internalUrl = (env.KEYCLOAK_INTERNAL_URL ?? issuer).replace(/\/$/, "");
  const clientId = env.KEYCLOAK_CLIENT_ID ?? DEFAULT_CLIENT_ID;
  const appUrl = (env.NEXT_PUBLIC_APP_URL ?? DEFAULT_APP_URL).replace(/\/$/, "");
  return {
    issuer,
    internalUrl,
    clientId,
    appUrl,
    redirectUri: `${appUrl}/auth/callback`,
    postLogoutRedirectUri: `${appUrl}/`,
  };
}
