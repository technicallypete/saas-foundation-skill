import "server-only";

// Runtime configuration, read per request on the server. Product, domain and
// database identity are never compiled into the bundle and never exposed
// through NEXT_PUBLIC_* variables, so renaming the product is an environment
// change, not a rebuild.
export type RuntimeConfig = {
  productName: string;
  platformBaseDomain: string;
  customDomainCnameTarget: string;
  coreInternalUrl: string;
};

function required(name: string): string {
  const value = process.env[name];
  if (!value || value.trim() === "") {
    throw new Error(`${name} is required`);
  }
  return value;
}

export function runtimeConfig(): RuntimeConfig {
  return {
    productName: required("PRODUCT_NAME"),
    platformBaseDomain: required("PLATFORM_BASE_DOMAIN"),
    customDomainCnameTarget: required("CUSTOM_DOMAIN_CNAME_TARGET"),
    coreInternalUrl: required("CORE_INTERNAL_URL"),
  };
}
