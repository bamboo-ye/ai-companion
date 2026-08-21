import type { MetadataRoute } from "next";

export default function manifest(): MetadataRoute.Manifest {
  return {
    name: "伴AI",
    short_name: "伴AI",
    description: "懂陪伴，也能一起把事情做好。",
    start_url: "/",
    scope: "/",
    display: "standalone",
    background_color: "#fffaf3",
    theme_color: "#e9638a",
    orientation: "portrait",
    lang: "zh-CN",
    categories: ["productivity", "lifestyle"],
    icons: [
      {
        src: "/icons/logo.png",
        sizes: "1254x1254",
        type: "image/png",
        purpose: "any",
      },
      {
        src: "/icons/logo.png",
        sizes: "1254x1254",
        type: "image/png",
        purpose: "maskable",
      },
    ],
  };
}
