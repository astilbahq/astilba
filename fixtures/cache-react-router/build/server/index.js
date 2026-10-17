import { A as defaultMapRouteProperties, B as redirect, C as instrumentationResultMetaContext, G as stripBasename, H as removeTrailingSlash, M as getRoutePattern, P as isRouteErrorResponse, R as matchRoutesImpl, S as instrumentHandler, T as RouterContextProvider, U as replace, V as redirectDocument, _ as isDataWithResponseInit, a as SingleFetchRedirectSymbol, b as isRedirectStatusCode, h as getStaticContextFromError, i as NO_BODY_STATUS_CODES, k as createDataFunctionUrl, l as encode, m as createStaticHandler, s as decodeViaTurboStream, t as getManifestPath, u as escapeHtml, v as isMutationMethod, w as ErrorResponseImpl, x as isResponse, y as isRedirectResponse } from "./assets/fog-of-war-DD8hmS1H.js";
//#region ../../node_modules/.pnpm/cookie-es@3.1.1/node_modules/cookie-es/dist/index.mjs
function splitSetCookieString(cookiesString) {
	if (Array.isArray(cookiesString)) return cookiesString.flatMap((c) => splitSetCookieString(c));
	if (typeof cookiesString !== "string") return [];
	const cookiesStrings = [];
	let pos = 0;
	let start;
	let ch;
	let lastComma;
	let nextStart;
	let cookiesSeparatorFound;
	const skipWhitespace = () => {
		while (pos < cookiesString.length && /\s/.test(cookiesString.charAt(pos))) pos += 1;
		return pos < cookiesString.length;
	};
	const notSpecialChar = () => {
		ch = cookiesString.charAt(pos);
		return ch !== "=" && ch !== ";" && ch !== ",";
	};
	while (pos < cookiesString.length) {
		start = pos;
		cookiesSeparatorFound = false;
		while (skipWhitespace()) {
			ch = cookiesString.charAt(pos);
			if (ch === ",") {
				lastComma = pos;
				pos += 1;
				skipWhitespace();
				nextStart = pos;
				while (pos < cookiesString.length && notSpecialChar()) pos += 1;
				if (pos < cookiesString.length && cookiesString.charAt(pos) === "=") {
					cookiesSeparatorFound = true;
					pos = nextStart;
					cookiesStrings.push(cookiesString.slice(start, lastComma));
					start = pos;
				} else pos = lastComma + 1;
			} else pos += 1;
		}
		if (!cookiesSeparatorFound || pos >= cookiesString.length) cookiesStrings.push(cookiesString.slice(start));
	}
	return cookiesStrings;
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/mode.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
function isServerMode(value) {
	return value === "development" || value === "production" || value === "test";
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/dev.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
var globalDevServerHooksKey = "__reactRouterDevServerHooks";
function getDevServerHooks() {
	return globalThis[globalDevServerHooksKey];
}
function getBuildTimeHeader(request, headerName) {
	if (typeof process !== "undefined") try {
		if (process.env.hasOwnProperty("IS_RR_BUILD_REQUEST") && process.env.IS_RR_BUILD_REQUEST === "yes") return request.headers.get(headerName);
	} catch (e) {}
	return null;
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/entry.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
function createEntryRouteModules(manifest) {
	return Object.keys(manifest).reduce((memo, routeId) => {
		let route = manifest[routeId];
		if (route) memo[routeId] = route.module;
		return memo;
	}, {});
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/errors.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
/**
* This thing probably warrants some explanation.
*
* The whole point here is to emulate componentDidCatch for server rendering and
* data loading. It can get tricky. React can do this on component boundaries
* but doesn't support it for server rendering or data loading. We know enough
* with nested routes to be able to emulate the behavior (because we know them
* statically before rendering.)
*
* Each route can export an `ErrorBoundary`.
*
* - When rendering throws an error, the nearest error boundary will render
*   (normal react componentDidCatch). This will be the route's own boundary, but
*   if none is provided, it will bubble up to the parents.
* - When data loading throws an error, the nearest error boundary will render
* - When performing an action, the nearest error boundary for the action's
*   route tree will render (no redirect happens)
*
* During normal react rendering, we do nothing special, just normal
* componentDidCatch.
*
* For server rendering, we mutate `renderBoundaryRouteId` to know the last
* layout that has an error boundary that tried to render. This emulates which
* layout would catch a thrown error. If the rendering fails, we catch the error
* on the server, and go again a second time with the emulator holding on to the
* information it needs to render the same error boundary as a dynamically
* thrown render error.
*
* When data loading, server or client side, we use the emulator to likewise
* hang on to the error and re-render at the appropriate layout (where a thrown
* error would have been caught by cDC).
*
* When actions throw, it all works the same. There's an edge case to be aware
* of though. Actions normally are required to redirect, but in the case of
* errors, we render the action's route with the emulator holding on to the
* error. If during this render a parent route/loader throws we ignore that new
* error and render the action's original error as deeply as possible. In other
* words, we simply ignore the new error and use the action's error in place
* because it came first, and that just wouldn't be fair to let errors cut in
* line.
*/
function sanitizeError(error, serverMode) {
	if (error instanceof Error && serverMode !== "development") {
		let sanitized = /* @__PURE__ */ new Error("Unexpected Server Error");
		sanitized.stack = void 0;
		return sanitized;
	}
	return error;
}
function sanitizeErrors(errors, serverMode) {
	return Object.entries(errors).reduce((acc, [routeId, error]) => {
		return Object.assign(acc, { [routeId]: sanitizeError(error, serverMode) });
	}, {});
}
function serializeError(error, serverMode) {
	let sanitized = sanitizeError(error, serverMode);
	return {
		message: sanitized.message,
		stack: sanitized.stack
	};
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/invariant.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
function invariant(value, message) {
	if (value === false || value === null || typeof value === "undefined") {
		console.error("The following error is a bug in React Router; please open an issue! https://github.com/remix-run/react-router/issues/new/choose");
		throw new Error(message);
	}
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/routeMatching.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
function matchServerRoutes(manifest, dataRoutes, branches, pathname, basename) {
	let matches = matchRoutesImpl(dataRoutes, pathname, basename ?? "/", false, branches);
	if (!matches) return null;
	return matches.map((match) => {
		let route = manifest[match.route.id];
		invariant(route, `Route with id "${match.route.id}" not found in manifest.`);
		return {
			params: match.params,
			pathname: match.pathname,
			route
		};
	});
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/data.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
async function callRouteHandler(handler, args) {
	let result = await handler({
		request: args.request,
		url: args.url,
		params: args.params,
		context: args.context,
		pattern: args.pattern
	});
	if (isDataWithResponseInit(result) && result.init && result.init.status && isRedirectStatusCode(result.init.status)) throw new Response(null, result.init);
	return result;
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/routes.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
function groupRoutesByParentId(manifest) {
	let routes = {};
	Object.values(manifest).forEach((route) => {
		if (route) {
			let parentId = route.parentId || "";
			if (!routes[parentId]) routes[parentId] = [];
			routes[parentId].push(route);
		}
	});
	return routes;
}
function createStaticHandlerDataRoutes(manifest, parentId = "", routesByParentId = groupRoutesByParentId(manifest)) {
	return (routesByParentId[parentId] || []).map((route) => {
		let commonRoute = {
			id: route.id,
			path: route.path,
			middleware: route.module.middleware,
			loader: route.module.loader ? async (args) => {
				let preRenderedData = getBuildTimeHeader(args.request, "X-React-Router-Prerender-Data");
				if (preRenderedData != null) {
					let encoded = preRenderedData ? decodeURI(preRenderedData) : preRenderedData;
					invariant(encoded, "Missing prerendered data for route");
					let uint8array = new TextEncoder().encode(encoded);
					let data = (await decodeViaTurboStream(new ReadableStream({ start(controller) {
						controller.enqueue(uint8array);
						controller.close();
					} }), global)).value;
					if (data && SingleFetchRedirectSymbol in data) {
						let result = data[SingleFetchRedirectSymbol];
						let init = { status: result.status };
						if (result.reload) throw redirectDocument(result.redirect, init);
						else if (result.replace) throw replace(result.redirect, init);
						else throw redirect(result.redirect, init);
					} else {
						invariant(data && route.id in data, "Unable to decode prerendered data");
						let result = data[route.id];
						invariant("data" in result, "Unable to process prerendered data");
						return result.data;
					}
				}
				return await callRouteHandler(route.module.loader, args);
			} : void 0,
			action: route.module.action ? (args) => callRouteHandler(route.module.action, args) : void 0,
			ErrorBoundary: route.id === "root" || route.module.ErrorBoundary != null ? () => null : void 0,
			handle: route.module.handle
		};
		return route.index ? {
			index: true,
			...commonRoute
		} : {
			caseSensitive: route.caseSensitive,
			children: createStaticHandlerDataRoutes(manifest, route.id, routesByParentId),
			...commonRoute
		};
	});
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/serverHandoff.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
function createServerHandoffString(serverHandoff) {
	return escapeHtml(JSON.stringify(serverHandoff));
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/headers.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
function getDocumentHeaders(context, build) {
	return getDocumentHeadersImpl(context, (m) => {
		let route = build.routes[m.route.id];
		invariant(route, `Route with id "${m.route.id}" not found in build`);
		return route.module.headers;
	});
}
function getDocumentHeadersImpl(context, getRouteHeadersFn, _defaultHeaders) {
	let boundaryIdx = context.errors ? context.matches.findIndex((m) => context.errors[m.route.id]) : -1;
	let matches = boundaryIdx >= 0 ? context.matches.slice(0, boundaryIdx + 1) : context.matches;
	let errorHeaders;
	if (boundaryIdx >= 0) {
		let { actionHeaders, actionData, loaderHeaders, loaderData } = context;
		context.matches.slice(boundaryIdx).some((match) => {
			let id = match.route.id;
			if (actionHeaders[id] && (!actionData || !actionData.hasOwnProperty(id))) errorHeaders = actionHeaders[id];
			else if (loaderHeaders[id] && !loaderData.hasOwnProperty(id)) errorHeaders = loaderHeaders[id];
			return errorHeaders != null;
		});
	}
	const defaultHeaders = new Headers(_defaultHeaders);
	return matches.reduce((parentHeaders, match, idx) => {
		let { id } = match.route;
		let loaderHeaders = context.loaderHeaders[id] || new Headers();
		let actionHeaders = context.actionHeaders[id] || new Headers();
		let includeErrorHeaders = errorHeaders != null && idx === matches.length - 1;
		let includeErrorCookies = includeErrorHeaders && errorHeaders !== loaderHeaders && errorHeaders !== actionHeaders;
		let headersFn = getRouteHeadersFn(match);
		if (headersFn == null) {
			let headers = new Headers(parentHeaders);
			if (includeErrorCookies) prependCookies(errorHeaders, headers);
			prependCookies(actionHeaders, headers);
			prependCookies(loaderHeaders, headers);
			return headers;
		}
		let headers = new Headers(typeof headersFn === "function" ? headersFn({
			loaderHeaders,
			parentHeaders,
			actionHeaders,
			errorHeaders: includeErrorHeaders ? errorHeaders : void 0
		}) : headersFn);
		if (includeErrorCookies) prependCookies(errorHeaders, headers);
		prependCookies(actionHeaders, headers);
		prependCookies(loaderHeaders, headers);
		prependCookies(parentHeaders, headers);
		return headers;
	}, new Headers(defaultHeaders));
}
function prependCookies(parentHeaders, childHeaders) {
	let parentSetCookieString = parentHeaders.get("Set-Cookie");
	if (parentSetCookieString) {
		let cookies = splitSetCookieString(parentSetCookieString);
		let childCookies = new Set(childHeaders.getSetCookie());
		cookies.forEach((cookie) => {
			if (!childCookies.has(cookie)) childHeaders.append("Set-Cookie", cookie);
		});
	}
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/actions.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
function throwIfPotentialCSRFAttack(request, allowedActionOrigins) {
	let originHeader = request.headers.get("origin");
	let originDomain = null;
	try {
		originDomain = typeof originHeader === "string" && originHeader !== "null" ? new URL(originHeader).host : originHeader;
	} catch {
		throw new Error(`\`origin\` header is not a valid URL. Aborting the action.`);
	}
	let host = new URL(request.url).host;
	if (originDomain && originDomain !== host) {
		if (!isAllowedOrigin(originDomain, allowedActionOrigins)) throw new Error("The `request.url` host does not match `origin` header from a forwarded action request. Aborting the action.");
	}
}
function matchWildcardDomain(domain, pattern) {
	const domainParts = domain.split(".");
	const patternParts = pattern.split(".");
	if (patternParts.length < 1) return false;
	if (domainParts.length < patternParts.length) return false;
	while (patternParts.length) {
		const patternPart = patternParts.pop();
		const domainPart = domainParts.pop();
		switch (patternPart) {
			case "": return false;
			case "*": if (domainPart) continue;
			else return false;
			case "**":
				if (patternParts.length > 0) return false;
				return domainPart !== void 0;
			case void 0:
			default: if (domainPart !== patternPart) return false;
		}
	}
	return domainParts.length === 0;
}
function isAllowedOrigin(originDomain, allowedActionOrigins = []) {
	return allowedActionOrigins.some((allowedOrigin) => allowedOrigin && (allowedOrigin === originDomain || matchWildcardDomain(originDomain, allowedOrigin)));
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/urls.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
function getNormalizedPath(request) {
	let url = new URL(request.url);
	let pathname = url.pathname;
	if (pathname.endsWith("/_.data")) pathname = pathname.replace(/_\.data$/, "");
	else pathname = pathname.replace(/\.data$/, "");
	let searchParams = new URLSearchParams(url.search);
	searchParams.delete("_routes");
	let search = searchParams.toString();
	if (search) search = `?${search}`;
	return {
		pathname,
		search,
		hash: ""
	};
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/single-fetch.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
var SERVER_NO_BODY_STATUS_CODES = /* @__PURE__ */ new Set([...NO_BODY_STATUS_CODES, 304]);
async function singleFetchAction(build, serverMode, staticHandler, request, loadContext, handleError) {
	try {
		try {
			throwIfPotentialCSRFAttack(request, Array.isArray(build.allowedActionOrigins) ? build.allowedActionOrigins : []);
		} catch (e) {
			return handleQueryError(/* @__PURE__ */ new Error("Bad Request"), 400);
		}
		return handleQueryResult(await staticHandler.query(request, {
			requestContext: loadContext,
			skipLoaderErrorBubbling: true,
			skipRevalidation: true,
			generateMiddlewareResponse: async (query) => {
				try {
					return handleQueryResult(await query(request));
				} catch (error) {
					return handleQueryError(error);
				}
			},
			normalizePath: (r) => getNormalizedPath(r)
		}));
	} catch (error) {
		return handleQueryError(error);
	}
	function handleQueryResult(result) {
		return isResponse(result) ? result : staticContextToResponse(result);
	}
	function handleQueryError(error, status = 500) {
		handleError(error);
		return generateSingleFetchResponse(request, build, serverMode, {
			result: { error },
			headers: new Headers(),
			status
		});
	}
	function staticContextToResponse(context) {
		let headers = getDocumentHeaders(context, build);
		if (isRedirectStatusCode(context.statusCode) && headers.has("Location")) return new Response(null, {
			status: context.statusCode,
			headers
		});
		if (context.errors) {
			Object.values(context.errors).forEach((err) => {
				if (!isRouteErrorResponse(err) || err.error) handleError(err);
			});
			context.errors = sanitizeErrors(context.errors, serverMode);
		}
		let singleFetchResult;
		if (context.errors) singleFetchResult = { error: Object.values(context.errors)[0] };
		else singleFetchResult = { data: Object.values(context.actionData || {})[0] };
		return generateSingleFetchResponse(request, build, serverMode, {
			result: singleFetchResult,
			headers,
			status: context.statusCode
		});
	}
}
async function singleFetchLoaders(build, serverMode, staticHandler, request, loadContext, handleError) {
	let routesParam = new URL(request.url).searchParams.get("_routes");
	let loadRouteIds = routesParam ? new Set(routesParam.split(",")) : null;
	try {
		return handleQueryResult(await staticHandler.query(request, {
			requestContext: loadContext,
			filterMatchesToLoad: (m) => !loadRouteIds || loadRouteIds.has(m.route.id),
			skipLoaderErrorBubbling: true,
			generateMiddlewareResponse: async (query) => {
				try {
					return handleQueryResult(await query(request));
				} catch (error) {
					return handleQueryError(error);
				}
			},
			normalizePath: (r) => getNormalizedPath(r)
		}));
	} catch (error) {
		return handleQueryError(error);
	}
	function handleQueryResult(result) {
		return isResponse(result) ? result : staticContextToResponse(result);
	}
	function handleQueryError(error) {
		handleError(error);
		return generateSingleFetchResponse(request, build, serverMode, {
			result: { error },
			headers: new Headers(),
			status: 500
		});
	}
	function staticContextToResponse(context) {
		let headers = getDocumentHeaders(context, build);
		if (isRedirectStatusCode(context.statusCode) && headers.has("Location")) return new Response(null, {
			status: context.statusCode,
			headers
		});
		if (context.errors) {
			Object.values(context.errors).forEach((err) => {
				if (!isRouteErrorResponse(err) || err.error) handleError(err);
			});
			context.errors = sanitizeErrors(context.errors, serverMode);
		}
		let results = {};
		let loadedMatches = new Set(context.matches.filter((m) => loadRouteIds ? loadRouteIds.has(m.route.id) : m.route.loader != null).map((m) => m.route.id));
		if (context.errors) for (let [id, error] of Object.entries(context.errors)) results[id] = { error };
		for (let [id, data] of Object.entries(context.loaderData)) if (!(id in results) && loadedMatches.has(id)) results[id] = { data };
		return generateSingleFetchResponse(request, build, serverMode, {
			result: results,
			headers,
			status: context.statusCode
		});
	}
}
function generateSingleFetchResponse(request, build, serverMode, { result, headers, status }) {
	let resultHeaders = new Headers(headers);
	resultHeaders.set("X-Remix-Response", "yes");
	if (SERVER_NO_BODY_STATUS_CODES.has(status)) return new Response(null, {
		status,
		headers: resultHeaders
	});
	resultHeaders.set("Content-Type", "text/x-script");
	resultHeaders.delete("Content-Length");
	return new Response(encodeViaTurboStream(result, request.signal, build.entry.module.streamTimeout, serverMode), {
		status: status || 200,
		headers: resultHeaders
	});
}
function generateSingleFetchRedirectResponse(redirectResponse, request, build, serverMode) {
	let redirect = getSingleFetchRedirect(redirectResponse.status, redirectResponse.headers, build.basename);
	let headers = new Headers(redirectResponse.headers);
	headers.delete("Location");
	headers.set("Content-Type", "text/x-script");
	return generateSingleFetchResponse(request, build, serverMode, {
		result: request.method === "GET" ? { [SingleFetchRedirectSymbol]: redirect } : redirect,
		headers,
		status: 202
	});
}
function getSingleFetchRedirect(status, headers, basename) {
	let redirect = headers.get("Location");
	if (basename) redirect = stripBasename(redirect, basename) || redirect;
	return {
		redirect,
		status,
		revalidate: headers.has("X-Remix-Revalidate") || headers.has("Set-Cookie"),
		reload: headers.has("X-Remix-Reload-Document"),
		replace: headers.has("X-Remix-Replace")
	};
}
function encodeViaTurboStream(data, requestSignal, streamTimeout, serverMode) {
	let controller = new AbortController();
	let timeoutId = setTimeout(() => {
		controller.abort(/* @__PURE__ */ new Error("Server Timeout"));
		cleanupCallbacks();
	}, typeof streamTimeout === "number" ? streamTimeout : 4950);
	let abortControllerOnRequestAbort = () => {
		controller.abort(requestSignal.reason);
		cleanupCallbacks();
	};
	requestSignal.addEventListener("abort", abortControllerOnRequestAbort);
	let cleanupCallbacks = () => {
		clearTimeout(timeoutId);
		requestSignal.removeEventListener("abort", abortControllerOnRequestAbort);
	};
	return encode(data, {
		signal: controller.signal,
		onComplete: cleanupCallbacks,
		plugins: [(value) => {
			if (value instanceof Error) {
				let { name, message, stack } = serverMode === "production" ? sanitizeError(value, serverMode) : value;
				return [
					"SanitizedError",
					name,
					message,
					stack
				];
			}
			if (value instanceof ErrorResponseImpl) {
				let { data, status, statusText } = value;
				return [
					"ErrorResponse",
					data,
					status,
					statusText
				];
			}
			if (value && typeof value === "object" && SingleFetchRedirectSymbol in value) return ["SingleFetchRedirect", value[SingleFetchRedirectSymbol]];
		}],
		postPlugins: [(value) => {
			if (!value) return;
			if (typeof value !== "object") return;
			return ["SingleFetchClassInstance", Object.fromEntries(Object.entries(value))];
		}, () => ["SingleFetchFallback"]]
	});
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/server.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
function derive(build, mode) {
	let dataRoutes = createStaticHandlerDataRoutes(build.routes);
	let serverMode = isServerMode(mode) ? mode : "production";
	let staticHandler = createStaticHandler(dataRoutes, {
		basename: build.basename,
		mapRouteProperties: defaultMapRouteProperties,
		instrumentations: build.entry.module.instrumentations,
		future: build.future
	});
	let errorHandler = build.entry.module.handleError || ((error, { request }) => {
		if (serverMode !== "test" && !request.signal.aborted) console.error(isRouteErrorResponse(error) && error.error ? error.error : error);
	});
	let requestHandlerInstrumentations = build.entry.module.instrumentations?.map((i) => i.handler).filter(Boolean);
	let requestHandler = async (request, initialContext) => {
		let params = {};
		let loadContext;
		let handleError = (error) => {
			if (mode === "development") getDevServerHooks()?.processRequestError?.(error);
			errorHandler(error, {
				context: loadContext,
				params,
				request
			});
		};
		if (initialContext && !(initialContext instanceof RouterContextProvider)) {
			let error = /* @__PURE__ */ new Error("Invalid `context` value provided to `handleRequest`. You must return an instance of `RouterContextProvider` from your `getLoadContext` function.");
			handleError(error);
			return returnLastResortErrorResponse(error, serverMode);
		}
		loadContext = initialContext || new RouterContextProvider();
		let requestUrl = new URL(request.url);
		let normalizedPath = getNormalizedPath(request);
		let normalizedPathname = normalizedPath.pathname;
		let isSpaMode = getBuildTimeHeader(request, "X-React-Router-SPA-Mode") === "yes";
		if (!build.ssr) {
			let decodedPath = decodeURI(normalizedPathname);
			if (build.basename && build.basename !== "/") {
				let strippedPath = stripBasename(decodedPath, build.basename);
				if (strippedPath == null) {
					errorHandler(new ErrorResponseImpl(404, "Not Found", `Refusing to prerender the \`${decodedPath}\` path because it does not start with the basename \`${build.basename}\``), {
						context: loadContext,
						params,
						request
					});
					return new Response("Not Found", {
						status: 404,
						statusText: "Not Found"
					});
				}
				decodedPath = strippedPath;
			}
			if (build.prerender.length === 0) isSpaMode = true;
			else if (!build.prerender.some((p) => removeTrailingSlash(p) === removeTrailingSlash(decodedPath))) if (requestUrl.pathname.endsWith(".data")) {
				errorHandler(new ErrorResponseImpl(404, "Not Found", `Refusing to SSR the path \`${decodedPath}\` because \`ssr:false\` is set and the path is not included in the \`prerender\` config, so in production the path will be a 404.`), {
					context: loadContext,
					params,
					request
				});
				return new Response("Not Found", {
					status: 404,
					statusText: "Not Found"
				});
			} else isSpaMode = true;
		}
		let manifestUrl = getManifestPath(build.routeDiscovery.manifestPath, build.basename);
		if (build.routeDiscovery.mode === "lazy" && requestUrl.pathname === manifestUrl) try {
			return await handleManifestRequest(build, staticHandler.dataRoutes, staticHandler._internalRouteBranches, requestUrl);
		} catch (e) {
			handleError(e);
			return new Response("Unknown Server Error", { status: 500 });
		}
		let matches = matchServerRoutes(build.routes, staticHandler.dataRoutes, staticHandler._internalRouteBranches, normalizedPathname, build.basename);
		if (matches && matches.length > 0) Object.assign(params, matches[0].params);
		if (requestHandlerInstrumentations?.length) loadContext.set(instrumentationResultMetaContext, {
			url: createDataFunctionUrl(request, normalizedPath),
			pattern: matches ? getRoutePattern(matches) : "",
			params: matches?.[0]?.params ? { ...matches[0].params } : {}
		});
		let response;
		if (requestUrl.pathname.endsWith(".data")) {
			response = await handleSingleFetchRequest(serverMode, build, staticHandler, request, loadContext, handleError);
			if (isRedirectResponse(response)) response = generateSingleFetchRedirectResponse(response, request, build, serverMode);
			if (build.entry.module.handleDataRequest) {
				response = await build.entry.module.handleDataRequest(response, {
					context: loadContext,
					params: matches ? matches[0].params : {},
					request
				});
				if (isRedirectResponse(response)) response = generateSingleFetchRedirectResponse(response, request, build, serverMode);
			}
		} else if (!isSpaMode && matches && matches[matches.length - 1].route.module.default == null && matches[matches.length - 1].route.module.ErrorBoundary == null) response = await handleResourceRequest(serverMode, build, staticHandler, matches.slice(-1)[0].route.id, request, loadContext, handleError);
		else {
			let { pathname } = requestUrl;
			let criticalCss = void 0;
			if (build.unstable_getCriticalCss) criticalCss = await build.unstable_getCriticalCss({ pathname });
			else if (mode === "development" && getDevServerHooks()?.getCriticalCss) criticalCss = await getDevServerHooks()?.getCriticalCss?.(pathname);
			response = await handleDocumentRequest(serverMode, build, staticHandler, request, loadContext, handleError, isSpaMode, criticalCss);
		}
		if (request.method === "HEAD") return new Response(null, {
			headers: response.headers,
			status: response.status,
			statusText: response.statusText
		});
		return response;
	};
	if (requestHandlerInstrumentations?.length) requestHandler = instrumentHandler(requestHandler, requestHandlerInstrumentations);
	return {
		serverMode,
		staticHandler,
		errorHandler,
		requestHandler
	};
}
/**
* Creates a request handler for a React Router server build.
*
* This is a low-level API used by server adapters to translate incoming
* requests into React Router responses.
*
* @category Utils
* @param build The server build, or a function that resolves to the server
* build, used to handle requests.
* @param mode The mode in which the server build is running.
* @returns A request handler that returns a response for each incoming request.
*/
var createRequestHandler = (build, mode) => {
	let _build;
	let serverMode;
	let staticHandler;
	let errorHandler;
	let _requestHandler;
	return async function requestHandler(request, initialContext) {
		_build = typeof build === "function" ? await build() : build;
		if (typeof build === "function") {
			let derived = derive(_build, mode);
			serverMode = derived.serverMode;
			staticHandler = derived.staticHandler;
			errorHandler = derived.errorHandler;
			_requestHandler = derived.requestHandler;
		} else if (!serverMode || !staticHandler || !errorHandler || !_requestHandler) {
			let derived = derive(_build, mode);
			serverMode = derived.serverMode;
			staticHandler = derived.staticHandler;
			errorHandler = derived.errorHandler;
			_requestHandler = derived.requestHandler;
		}
		return _requestHandler(request, initialContext);
	};
};
async function handleManifestRequest(build, dataRoutes, branches, url) {
	if (url.toString().length > 7680) return new Response(null, {
		statusText: "Bad Request",
		status: 400
	});
	if (build.assets.version !== url.searchParams.get("version")) return new Response(null, {
		status: 204,
		headers: { "X-Remix-Reload-Document": "true" }
	});
	let patches = {};
	if (url.searchParams.has("paths")) {
		let pathParam = url.searchParams.get("paths") || "";
		let paths = new Set(pathParam.split(",").filter(Boolean));
		for (let path of paths) {
			if (!path.startsWith("/")) path = `/${path}`;
			let matches = matchServerRoutes(build.routes, dataRoutes, branches, path, build.basename);
			if (matches) for (let match of matches) {
				let routeId = match.route.id;
				let route = build.assets.routes[routeId];
				if (route) patches[routeId] = route;
			}
		}
		return Response.json(patches, { headers: { "Cache-Control": "public, max-age=31536000, immutable" } });
	}
	return new Response("Invalid Request", { status: 400 });
}
async function handleSingleFetchRequest(serverMode, build, staticHandler, request, loadContext, handleError) {
	return isMutationMethod(request.method) ? await singleFetchAction(build, serverMode, staticHandler, request, loadContext, handleError) : await singleFetchLoaders(build, serverMode, staticHandler, request, loadContext, handleError);
}
async function handleDocumentRequest(serverMode, build, staticHandler, request, loadContext, handleError, isSpaMode, criticalCss) {
	try {
		if (isMutationMethod(request.method)) try {
			throwIfPotentialCSRFAttack(request, Array.isArray(build.allowedActionOrigins) ? build.allowedActionOrigins : []);
		} catch (e) {
			handleError(e);
			return new Response("Bad Request", { status: 400 });
		}
		let result = await staticHandler.query(request, {
			requestContext: loadContext,
			generateMiddlewareResponse: async (query) => {
				try {
					let innerResult = await query(request);
					if (!isResponse(innerResult)) innerResult = await renderHtml(innerResult, isSpaMode);
					return innerResult;
				} catch (error) {
					handleError(error);
					return new Response(null, { status: 500 });
				}
			},
			normalizePath: (r) => getNormalizedPath(r)
		});
		if (!isResponse(result)) result = await renderHtml(result, isSpaMode);
		return result;
	} catch (error) {
		handleError(error);
		return new Response(null, { status: 500 });
	}
	async function renderHtml(context, isSpaMode) {
		let headers = getDocumentHeaders(context, build);
		if (SERVER_NO_BODY_STATUS_CODES.has(context.statusCode)) return new Response(null, {
			status: context.statusCode,
			headers
		});
		if (context.errors) {
			Object.values(context.errors).forEach((err) => {
				if (!isRouteErrorResponse(err) || err.error) handleError(err);
			});
			context.errors = sanitizeErrors(context.errors, serverMode);
		}
		let state = {
			loaderData: context.loaderData,
			actionData: context.actionData,
			errors: context.errors
		};
		let baseServerHandoff = {
			basename: build.basename,
			future: build.future,
			routeDiscovery: build.routeDiscovery,
			ssr: build.ssr,
			isSpaMode
		};
		let entryContext = {
			manifest: build.assets,
			branches: staticHandler._internalRouteBranches,
			routeModules: createEntryRouteModules(build.routes),
			staticHandlerContext: context,
			criticalCss,
			serverHandoffString: createServerHandoffString({
				...baseServerHandoff,
				criticalCss
			}),
			serverHandoffStream: encodeViaTurboStream(state, request.signal, build.entry.module.streamTimeout, serverMode),
			renderMeta: {},
			future: build.future,
			ssr: build.ssr,
			routeDiscovery: build.routeDiscovery,
			isSpaMode,
			serializeError: (err) => serializeError(err, serverMode)
		};
		let handleDocumentRequestFunction = build.entry.module.default;
		try {
			return await handleDocumentRequestFunction(request, context.statusCode, headers, entryContext, loadContext);
		} catch (error) {
			handleError(error);
			let errorForSecondRender = error;
			if (isResponse(error)) try {
				let data = await unwrapResponse(error);
				errorForSecondRender = new ErrorResponseImpl(error.status, error.statusText, data);
			} catch (e) {}
			context = getStaticContextFromError(staticHandler.dataRoutes, context, errorForSecondRender);
			if (context.errors) context.errors = sanitizeErrors(context.errors, serverMode);
			let state = {
				loaderData: context.loaderData,
				actionData: context.actionData,
				errors: context.errors
			};
			entryContext = {
				...entryContext,
				staticHandlerContext: context,
				serverHandoffString: createServerHandoffString(baseServerHandoff),
				serverHandoffStream: encodeViaTurboStream(state, request.signal, build.entry.module.streamTimeout, serverMode),
				renderMeta: {}
			};
			try {
				return await handleDocumentRequestFunction(request, context.statusCode, headers, entryContext, loadContext);
			} catch (error) {
				handleError(error);
				return returnLastResortErrorResponse(error, serverMode);
			}
		}
	}
}
async function handleResourceRequest(serverMode, build, staticHandler, routeId, request, loadContext, handleError) {
	try {
		return handleQueryRouteResult(await staticHandler.queryRoute(request, {
			routeId,
			requestContext: loadContext,
			generateMiddlewareResponse: async (queryRoute) => {
				try {
					return handleQueryRouteResult(await queryRoute(request));
				} catch (error) {
					return handleQueryRouteError(error);
				}
			},
			normalizePath: (r) => getNormalizedPath(r)
		}));
	} catch (error) {
		return handleQueryRouteError(error);
	}
	function handleQueryRouteResult(result) {
		if (isResponse(result)) return result;
		if (typeof result === "string") return new Response(result);
		return Response.json(result);
	}
	function handleQueryRouteError(error) {
		if (isResponse(error)) return error;
		if (isRouteErrorResponse(error)) {
			handleError(error);
			return errorResponseToJson(error, serverMode);
		}
		if (error instanceof Error && error.message === "Expected a response from queryRoute") {
			let newError = /* @__PURE__ */ new Error("Expected a Response to be returned from resource route handler");
			handleError(newError);
			return returnLastResortErrorResponse(newError, serverMode);
		}
		handleError(error);
		return returnLastResortErrorResponse(error, serverMode);
	}
}
function errorResponseToJson(errorResponse, serverMode) {
	return Response.json(serializeError(errorResponse.error || /* @__PURE__ */ new Error("Unexpected Server Error"), serverMode), {
		status: errorResponse.status,
		statusText: errorResponse.statusText
	});
}
function returnLastResortErrorResponse(error, serverMode) {
	let message = "Unexpected Server Error";
	if (serverMode !== "production") message += `\n\n${String(error)}`;
	return new Response(message, {
		status: 500,
		headers: { "Content-Type": "text/plain" }
	});
}
function unwrapResponse(response) {
	let contentType = response.headers.get("Content-Type");
	return contentType && /\bapplication\/json\b/.test(contentType) ? response.body == null ? null : response.json() : response.text();
}
//#endregion
//#region workers/app.ts
var handler = createRequestHandler(() => import("./assets/server-build-G8fEm4ZH.js"), "production");
//#endregion
//#region \0virtual:cloudflare/worker-entry
var worker_entry_default = { fetch(request) {
	return handler(request);
} };
//#endregion
export { worker_entry_default as default };
