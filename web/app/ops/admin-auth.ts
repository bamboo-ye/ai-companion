export type AdminCredentials = { session: true };
export const adminCredentials: AdminCredentials = { session: true };
export function adminHeaders(credentials: AdminCredentials): Record<string, string> {
  if (!credentials.session) throw new Error("请先登录管理后台");
  return { "X-Admin-CSRF": "1" };
}

type PublicKeyJSON = Record<string, unknown> & { challenge: string; user?: { id: string; name: string; displayName: string }; excludeCredentials?: DescriptorJSON[]; allowCredentials?: DescriptorJSON[] };
type DescriptorJSON = { id: string; type: PublicKeyCredentialType; transports?: AuthenticatorTransport[] };
type APIError = { code?: string; message?: string };

export function fromBase64URL(value: string): ArrayBuffer {
  const raw = atob(value.replace(/-/g, "+").replace(/_/g, "/"));
  return Uint8Array.from(raw, (char) => char.charCodeAt(0)).buffer;
}
function toBase64URL(value: ArrayBuffer): string {
  return btoa(String.fromCharCode(...new Uint8Array(value))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}
function descriptor(value: DescriptorJSON): PublicKeyCredentialDescriptor { return { ...value, id: fromBase64URL(value.id) }; }
export function passkeySupported(): boolean { return typeof window !== "undefined" && window.isSecureContext && typeof PublicKeyCredential !== "undefined"; }

function credentialJSON(credential: PublicKeyCredential) {
  const response = credential.response;
  const common = { id: credential.id, rawId: toBase64URL(credential.rawId), type: credential.type, authenticatorAttachment: credential.authenticatorAttachment, clientExtensionResults: credential.getClientExtensionResults() };
  if (response instanceof AuthenticatorAttestationResponse) return { ...common, response: { clientDataJSON: toBase64URL(response.clientDataJSON), attestationObject: toBase64URL(response.attestationObject), transports: response.getTransports?.() ?? [] } };
  const assertion = response as AuthenticatorAssertionResponse;
  return { ...common, response: { clientDataJSON: toBase64URL(assertion.clientDataJSON), authenticatorData: toBase64URL(assertion.authenticatorData), signature: toBase64URL(assertion.signature), userHandle: assertion.userHandle ? toBase64URL(assertion.userHandle) : null } };
}

function url(path: string): string {
  if (!path.startsWith("/v1/ops/")) throw new Error("无效的后台 API 路径");
  return `/api/admin/${path.slice("/v1/ops/".length)}`;
}
async function rawFetch(path: string, init: RequestInit = {}): Promise<Response> {
  const headers = new Headers(init.headers);
  headers.delete("Authorization");
  headers.set("X-Admin-CSRF", "1");
  return fetch(url(path), { ...init, headers, credentials: "same-origin", cache: "no-store" });
}
async function authJSON<T>(path: string, body: unknown): Promise<T> {
  const response = await rawFetch(`/v1/ops/auth/${path}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const payload = await response.json() as T & APIError;
  if (!response.ok) throw new Error(payload.message ?? "通行密钥验证失败");
  return payload;
}

export async function loginWithPasskey(): Promise<void> {
  if (!passkeySupported()) throw new Error("请使用支持通行密钥的浏览器，通过 HTTPS 或 localhost 打开后台");
  const { publicKey } = await authJSON<{ publicKey: PublicKeyJSON }>("passkey/login/options", {});
  const options = { ...publicKey, challenge: fromBase64URL(publicKey.challenge), allowCredentials: publicKey.allowCredentials?.map(descriptor), userVerification: "required" } as PublicKeyCredentialRequestOptions;
  const credential = await navigator.credentials.get({ publicKey: options }) as PublicKeyCredential | null;
  if (!credential) throw new Error("未完成通行密钥验证");
  await authJSON("passkey/login/verify", credentialJSON(credential));
}

export async function registerPasskey(invite = ""): Promise<void> {
  if (!passkeySupported()) throw new Error("请使用支持通行密钥的浏览器，通过 HTTPS 或 localhost 打开后台");
  const response = await adminFetch("/v1/ops/auth/passkey/register/options", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ invite }) });
  const payload = await response.json() as { publicKey: PublicKeyJSON } & APIError;
  if (!response.ok) throw new Error(payload.message ?? "无法开始绑定");
  const publicKey = payload.publicKey;
  if (!publicKey.user) throw new Error("绑定选项无效");
  const options = { ...publicKey, challenge: fromBase64URL(publicKey.challenge), user: { ...publicKey.user, id: fromBase64URL(publicKey.user.id) }, excludeCredentials: publicKey.excludeCredentials?.map(descriptor) } as PublicKeyCredentialCreationOptions;
  const credential = await navigator.credentials.create({ publicKey: options }) as PublicKeyCredential | null;
  if (!credential) throw new Error("未完成通行密钥绑定");
  await authJSON("passkey/register/verify", credentialJSON(credential));
}

let reauthentication: Promise<void> | null = null;
export async function adminFetch(path: string, init: RequestInit = {}): Promise<Response> {
  let response = await rawFetch(path, init);
  if (response.status === 401) {
    const error = await response.clone().json().catch(() => ({})) as APIError;
    if (error.code === "operator_reauth_required") {
      reauthentication ??= loginWithPasskey().finally(() => { reauthentication = null; });
      await reauthentication;
      response = await rawFetch(path, init);
    }
    if (response.status === 401 && typeof window !== "undefined") window.dispatchEvent(new Event("admin-session-expired"));
  }
  return response;
}
export async function logoutAdmin(): Promise<void> {
  const response = await rawFetch("/v1/ops/auth/logout", { method: "POST" });
  if (!response.ok) throw new Error("退出失败，请重试");
}
export function passkeyError(cause: unknown): string {
  if (cause instanceof DOMException) {
    if (cause.name === "NotAllowedError" || cause.name === "AbortError") return "验证已取消或超时，请重试；首次绑定邀请需要重新签发。";
    if (cause.name === "InvalidStateError") return "此设备已绑定该账号，请使用通行密钥登录。";
    if (cause.name === "SecurityError") return "当前域名与通行密钥配置不匹配，请使用管理员提供的后台地址。";
  }
  return cause instanceof Error ? cause.message : "通行密钥验证失败，请重试";
}
