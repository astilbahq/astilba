// Starlight 0.42 no longer ships declarations for its virtual modules.
declare module "virtual:starlight/user-config" {
  const Config: import("@astrojs/starlight/types").StarlightConfig;
  export default Config;
}

declare module "virtual:starlight/components/LanguageSelect" {
  const Component: typeof import("@astrojs/starlight/components/LanguageSelect.astro").default;
  export default Component;
}

declare module "virtual:starlight/components/Search" {
  const Component: typeof import("@astrojs/starlight/components/Search.astro").default;
  export default Component;
}

declare module "virtual:starlight/components/SiteTitle" {
  const Component: typeof import("@astrojs/starlight/components/SiteTitle.astro").default;
  export default Component;
}
