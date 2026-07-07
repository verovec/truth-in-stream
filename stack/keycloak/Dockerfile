# syntax=docker/dockerfile:1
# Optimized production Keycloak image. The builder stage runs `kc.sh build` with
# the Postgres store and health/metrics baked in, so the runtime stage starts
# with `--optimized` (no build step on boot). The prod realm is copied into the
# import directory and applied by `--import-realm` on first start (idempotent:
# an existing realm is skipped, so a realm change needs an explicit re-import).
# Pinned to the same version the local dev stack runs (docker-compose) so the
# realm import shape stays compatible across environments.
ARG KEYCLOAK_VERSION=26.6.3

FROM quay.io/keycloak/keycloak:${KEYCLOAK_VERSION} AS builder
# Build-time options: baking these into the optimized image means they do not
# need to be re-resolved at boot.
ENV KC_DB=postgres
ENV KC_HEALTH_ENABLED=true
ENV KC_METRICS_ENABLED=true
RUN /opt/keycloak/bin/kc.sh build

FROM quay.io/keycloak/keycloak:${KEYCLOAK_VERSION}
COPY --from=builder /opt/keycloak/ /opt/keycloak/
# Realm applied on first boot via --import-realm. All hostname/DB/proxy runtime
# configuration is injected as environment variables by the ECS task definition.
COPY realm-prod.json /opt/keycloak/data/import/realm.json
ENTRYPOINT ["/opt/keycloak/bin/kc.sh"]
CMD ["start", "--optimized", "--import-realm"]
