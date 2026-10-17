import { n as __exportAll, r as __toESM, t as __commonJSMin } from "./rolldown-runtime-B-1-B7_t.js";
import { D as convertRoutesToDataRoutes, E as convertRouteMatchToUiMatch, F as joinPaths, G as stripBasename, I as matchPath, J as invariant$1, K as require_react, L as matchRoutes, M as getRoutePattern, N as isBrowser$1, O as createContext, P as isRouteErrorResponse, W as resolveTo, X as warning, Y as parsePath, Z as ABSOLUTE_URL_REGEX, c as singleFetchUrl, d as IDLE_BLOCKER, f as IDLE_FETCHER, g as hasInvalidProtocol, j as getResolveToMatches, n as getPartialManifest, o as StreamTransfer, p as IDLE_NAVIGATION, q as createPath, r as isFogOfWarEnabled, u as escapeHtml, w as ErrorResponseImpl, z as parseToInfo } from "./fog-of-war-DD8hmS1H.js";
import { AsyncLocalStorage as AsyncLocalStorage$1 } from "node:async_hooks";
import { createHash, createHmac } from "node:crypto";
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/context.js
var import_react = /* @__PURE__ */ __toESM(require_react(), 1);
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
var DataRouterContext = import_react.createContext(null);
DataRouterContext.displayName = "DataRouter";
var DataRouterStateContext = import_react.createContext(null);
DataRouterStateContext.displayName = "DataRouterState";
var RSCRouterContext = import_react.createContext(false);
function useIsRSCRouterContext() {
	return import_react.useContext(RSCRouterContext);
}
var ViewTransitionContext = import_react.createContext({ isTransitioning: false });
ViewTransitionContext.displayName = "ViewTransition";
var FetchersContext = import_react.createContext(/* @__PURE__ */ new Map());
FetchersContext.displayName = "Fetchers";
var AwaitContext = import_react.createContext(null);
AwaitContext.displayName = "Await";
var NavigationContext = import_react.createContext(null);
NavigationContext.displayName = "Navigation";
var LocationContext = import_react.createContext(null);
LocationContext.displayName = "Location";
var RouteContext = import_react.createContext({
	outlet: null,
	matches: [],
	isDataRoute: false
});
RouteContext.displayName = "Route";
var RouteErrorContext = import_react.createContext(null);
RouteErrorContext.displayName = "RouteError";
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/errors.js
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
var ERROR_DIGEST_BASE = "REACT_ROUTER_ERROR";
var ERROR_DIGEST_REDIRECT = "REDIRECT";
var ERROR_DIGEST_ROUTE_ERROR_RESPONSE = "ROUTE_ERROR_RESPONSE";
function decodeRedirectErrorDigest(digest) {
	if (digest.startsWith(`${ERROR_DIGEST_BASE}:${ERROR_DIGEST_REDIRECT}:{`)) try {
		let parsed = JSON.parse(digest.slice(28));
		if (typeof parsed === "object" && parsed && typeof parsed.status === "number" && typeof parsed.statusText === "string" && typeof parsed.location === "string" && typeof parsed.reloadDocument === "boolean" && typeof parsed.replace === "boolean") return parsed;
	} catch {}
}
function decodeRouteErrorResponseDigest(digest) {
	if (digest.startsWith(`${ERROR_DIGEST_BASE}:${ERROR_DIGEST_ROUTE_ERROR_RESPONSE}:{`)) try {
		let parsed = JSON.parse(digest.slice(40));
		if (typeof parsed === "object" && parsed && typeof parsed.status === "number" && typeof parsed.statusText === "string") return new ErrorResponseImpl(parsed.status, parsed.statusText, parsed.data);
	} catch {}
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/hooks.js
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
* Resolves a URL against the current {@link Location}.
*
* @example
* import { useHref } from "react-router";
*
* function SomeComponent() {
*   let href = useHref("some/where");
*   // "/resolved/some/where"
* }
*
* @public
* @category Hooks
* @param to The path to resolve
* @param options Options
* @param options.relative Defaults to `"route"` so routing is relative to the
* route tree.
* Set to `"path"` to make relative routing operate against path segments.
* @returns The resolved href string
*/
function useHref(to, { relative } = {}) {
	invariant$1(useInRouterContext(), `useHref() may be used only in the context of a <Router> component.`);
	let { basename, navigator } = import_react.useContext(NavigationContext);
	let { hash, pathname, search } = useResolvedPath(to, { relative });
	let joinedPathname = pathname;
	if (basename !== "/") joinedPathname = pathname === "/" ? basename : joinPaths([basename, pathname]);
	return navigator.createHref({
		pathname: joinedPathname,
		search,
		hash
	});
}
/**
* Returns `true` if this component is a descendant of a {@link Router}, useful
* to ensure a component is used within a {@link Router}.
*
* @public
* @category Hooks
* @mode framework
* @mode data
* @returns Whether the component is within a {@link Router} context
*/
function useInRouterContext() {
	return import_react.useContext(LocationContext) != null;
}
/**
* Returns the current {@link Location}. This can be useful if you'd like to
* perform some side effect whenever it changes.
*
* @example
* import * as React from 'react'
* import { useLocation } from 'react-router'
*
* function SomeComponent() {
*   let location = useLocation()
*
*   React.useEffect(() => {
*     // Google Analytics
*     ga('send', 'pageview')
*   }, [location]);
*
*   return (
*     // ...
*   );
* }
*
* @public
* @category Hooks
* @returns The current {@link Location} object
*/
function useLocation() {
	invariant$1(useInRouterContext(), `useLocation() may be used only in the context of a <Router> component.`);
	return import_react.useContext(LocationContext).location;
}
var navigateEffectWarning = "You should call navigate() in a React.useEffect(), not when your component is first rendered.";
/**
* Returns a function that lets you navigate programmatically in the browser in
* response to user interactions or effects.
*
* It's often better to use {@link redirect} in [`action`](../../start/framework/route-module#action)/[`loader`](../../start/framework/route-module#loader)
* functions than this hook.
*
* The returned function signature is `navigate(to, options?)`/`navigate(delta)` where:
*
* * `to` can be a string path, a {@link To} object, or a number (delta)
* * `options` contains options for modifying the navigation
*   * These options work in all modes (Framework, Data, and Declarative):
*     * `relative`: `"route"` or `"path"` to control relative routing logic
*     * `replace`: Replace the current entry in the [`History`](https://developer.mozilla.org/en-US/docs/Web/API/History) stack
*     * `state`: Optional [`history.state`](https://developer.mozilla.org/en-US/docs/Web/API/History/state) to include with the new {@link Location}
*   * These options only work in Framework and Data modes:
*     * `flushSync`: Wrap the DOM updates in [`ReactDom.flushSync`](https://react.dev/reference/react-dom/flushSync)
*     * `preventScrollReset`: Do not scroll back to the top of the page after navigation
*     * `viewTransition`: Enable [`document.startViewTransition`](https://developer.mozilla.org/en-US/docs/Web/API/Document/startViewTransition) for this navigation
*
* @example
* import { useNavigate } from "react-router";
*
* function SomeComponent() {
*   let navigate = useNavigate();
*   return (
*     <button onClick={() => navigate(-1)}>
*       Go Back
*     </button>
*   );
* }
*
* @additionalExamples
* ### Navigate to another path
*
* ```tsx
* navigate("/some/route");
* navigate("/some/route?search=param");
* ```
*
* ### Navigate with a {@link To} object
*
* All properties are optional.
*
* ```tsx
* navigate(
*   {
*     pathname: "/some/route",
*     search: "?search=param",
*     hash: "#hash",
*   },
*   {
*     state: { some: "state" },
*   },
* );
* ```
*
* If you use `state`, that will be available on the {@link Location} object on
* the next page. Access it with `useLocation().state` (see {@link useLocation}).
*
* ### Navigate back or forward in the history stack
*
* ```tsx
* // back
* // often used to close modals
* navigate(-1);
*
* // forward
* // often used in a multistep wizard workflows
* navigate(1);
* ```
*
* Be cautious with `navigate(number)`. If your application can load up to a
* route that has a button that tries to navigate forward/back, there may not be
* a [`History`](https://developer.mozilla.org/en-US/docs/Web/API/History)
* entry to go back or forward to, or it can go somewhere you don't expect
* (like a different domain).
*
* Only use this if you're sure they will have an entry in the [`History`](https://developer.mozilla.org/en-US/docs/Web/API/History)
* stack to navigate to.
*
* ### Replace the current entry in the history stack
*
* This will remove the current entry in the [`History`](https://developer.mozilla.org/en-US/docs/Web/API/History)
* stack, replacing it with a new one, similar to a server side redirect.
*
* ```tsx
* navigate("/some/route", { replace: true });
* ```
*
* ### Prevent Scroll Reset
*
* [MODES: framework, data]
*
* <br/>
* <br/>
*
* To prevent {@link ScrollRestoration | `<ScrollRestoration>`} from resetting
* the scroll position, use the `preventScrollReset` option.
*
* ```tsx
* navigate("?some-tab=1", { preventScrollReset: true });
* ```
*
* For example, if you have a tab interface connected to search params in the
* middle of a page, and you don't want it to scroll to the top when a tab is
* clicked.
*
* ### Return Type Augmentation
*
* Internally, `useNavigate` uses a separate implementation when you are in
* Declarative mode versus Data/Framework mode - the primary difference being
* that the latter is able to return a stable reference that does not change
* identity across navigations. The implementation in Data/Framework mode also
* returns a [`Promise`](https://developer.mozilla.org/en-US/docs/Web/JavaScript/Reference/Global_Objects/Promise)
* that resolves when the navigation is completed. This means the return type of
* `useNavigate` is `void | Promise<void>`. This is accurate, but can lead to
* some red squigglies based on the union in the return value:
*
* - If you're using `typescript-eslint`, you may see errors from
*   [`@typescript-eslint/no-floating-promises`](https://typescript-eslint.io/rules/no-floating-promises)
* - In Framework/Data mode, `React.use(navigate())` will show a false-positive
*   `Argument of type 'void | Promise<void>' is not assignable to parameter of
*   type 'Usable<void>'` error
*
* The easiest way to work around these issues is to augment the type based on the
* router you're using:
*
* ```ts
* // If using <BrowserRouter>
* declare module "react-router" {
*   interface NavigateFunction {
*     (to: To, options?: NavigateOptions): void;
*     (delta: number): void;
*   }
* }
*
* // If using <RouterProvider> or Framework mode
* declare module "react-router" {
*   interface NavigateFunction {
*     (to: To, options?: NavigateOptions): Promise<void>;
*     (delta: number): Promise<void>;
*   }
* }
* ```
*
* @public
* @category Hooks
* @returns A navigate function for programmatic navigation
*/
function useNavigate() {
	let { isDataRoute } = import_react.useContext(RouteContext);
	return isDataRoute ? useNavigateStable() : useNavigateUnstable();
}
function useNavigateUnstable() {
	invariant$1(useInRouterContext(), `useNavigate() may be used only in the context of a <Router> component.`);
	let dataRouterContext = import_react.useContext(DataRouterContext);
	let { basename, navigator } = import_react.useContext(NavigationContext);
	let { matches } = import_react.useContext(RouteContext);
	let { pathname: locationPathname } = useLocation();
	let routePathnamesJson = JSON.stringify(getResolveToMatches(matches));
	let activeRef = import_react.useRef(false);
	import_react.useLayoutEffect(() => {
		activeRef.current = true;
	});
	return import_react.useCallback((to, options = {}) => {
		warning(activeRef.current, navigateEffectWarning);
		if (!activeRef.current) return;
		if (typeof to === "number") {
			navigator.go(to);
			return;
		}
		let path = resolveTo(to, JSON.parse(routePathnamesJson), locationPathname, options.relative === "path");
		if (dataRouterContext == null && basename !== "/") path.pathname = path.pathname === "/" ? basename : joinPaths([basename, path.pathname]);
		(!!options.replace ? navigator.replace : navigator.push)(path, options.state, options);
	}, [
		basename,
		navigator,
		routePathnamesJson,
		locationPathname,
		dataRouterContext
	]);
}
var OutletContext = import_react.createContext(null);
/**
* Returns the element for the child route at this level of the route
* hierarchy. Used internally by {@link Outlet | `<Outlet>`} to render child
* routes.
*
* @public
* @category Hooks
* @param context The context to pass to the outlet
* @returns The child route element or `null` if no child routes match
*/
function useOutlet(context) {
	let outlet = import_react.useContext(RouteContext).outlet;
	return import_react.useMemo(() => outlet && /* @__PURE__ */ import_react.createElement(OutletContext.Provider, { value: context }, outlet), [outlet, context]);
}
/**
* Returns an object of key/value-pairs of the dynamic params from the current
* URL that were matched by the routes. Child routes inherit all params from
* their parent routes.
*
* Assuming a route pattern like `/posts/:postId` is matched by `/posts/123`
* then `params.postId` will be `"123"`.
*
* @example
* import { useParams } from "react-router";
*
* function SomeComponent() {
*   let params = useParams();
*   params.postId;
* }
*
* @additionalExamples
* ### Basic Usage
*
* ```tsx
* import { useParams } from "react-router";
*
* // given a route like:
* <Route path="/posts/:postId" element={<Post />} />;
*
* // or a data route like:
* createBrowserRouter([
*   {
*     path: "/posts/:postId",
*     component: Post,
*   },
* ]);
*
* // or in routes.ts
* route("/posts/:postId", "routes/post.tsx");
* ```
*
* Access the params in a component:
*
* ```tsx
* import { useParams } from "react-router";
*
* export default function Post() {
*   let params = useParams();
*   return <h1>Post: {params.postId}</h1>;
* }
* ```
*
* ### Multiple Params
*
* Patterns can have multiple params:
*
* ```tsx
* "/posts/:postId/comments/:commentId";
* ```
*
* All will be available in the params object:
*
* ```tsx
* import { useParams } from "react-router";
*
* export default function Post() {
*   let params = useParams();
*   return (
*     <h1>
*       Post: {params.postId}, Comment: {params.commentId}
*     </h1>
*   );
* }
* ```
*
* ### Catchall Params
*
* Catchall params are defined with `*`:
*
* ```tsx
* "/files/*";
* ```
*
* The matched value will be available in the params object as follows:
*
* ```tsx
* import { useParams } from "react-router";
*
* export default function File() {
*   let params = useParams();
*   let catchall = params["*"];
*   // ...
* }
* ```
*
* You can destructure the catchall param:
*
* ```tsx
* export default function File() {
*   let { "*": catchall } = useParams();
*   console.log(catchall);
* }
* ```
*
* @public
* @category Hooks
* @returns An object containing the dynamic route parameters
*/
function useParams() {
	let { matches } = import_react.useContext(RouteContext);
	return matches[matches.length - 1]?.params ?? {};
}
/**
* Resolves the pathname of the given `to` value against the current
* {@link Location}. Similar to {@link useHref}, but returns a
* {@link Path} instead of a string.
*
* @example
* import { useResolvedPath } from "react-router";
*
* function SomeComponent() {
*   // if the user is at /dashboard/profile
*   let path = useResolvedPath("../accounts");
*   path.pathname; // "/dashboard/accounts"
*   path.search; // ""
*   path.hash; // ""
* }
*
* @public
* @category Hooks
* @param to The path to resolve
* @param options Options
* @param options.relative Defaults to `"route"` so routing is relative to the route tree.
*                         Set to `"path"` to make relative routing operate against path segments.
* @returns The resolved {@link Path} object with `pathname`, `search`, and `hash`
*/
function useResolvedPath(to, { relative } = {}) {
	let { matches } = import_react.useContext(RouteContext);
	let { pathname: locationPathname } = useLocation();
	let routePathnamesJson = JSON.stringify(getResolveToMatches(matches));
	return import_react.useMemo(() => resolveTo(to, JSON.parse(routePathnamesJson), locationPathname, relative === "path"), [
		to,
		routePathnamesJson,
		locationPathname,
		relative
	]);
}
function useRoutesImpl(routes, locationArg, dataRouterOpts) {
	invariant$1(useInRouterContext(), `useRoutes() may be used only in the context of a <Router> component.`);
	let { navigator } = import_react.useContext(NavigationContext);
	let { matches: parentMatches } = import_react.useContext(RouteContext);
	let routeMatch = parentMatches[parentMatches.length - 1];
	let parentParams = routeMatch ? routeMatch.params : {};
	routeMatch && routeMatch.pathname;
	let parentPathnameBase = routeMatch ? routeMatch.pathnameBase : "/";
	routeMatch && routeMatch.route;
	let locationFromContext = useLocation();
	let location;
	if (locationArg) {
		let parsedLocationArg = typeof locationArg === "string" ? parsePath(locationArg) : locationArg;
		invariant$1(parentPathnameBase === "/" || parsedLocationArg.pathname?.startsWith(parentPathnameBase), `When overriding the location using \`<Routes location>\` or \`useRoutes(routes, location)\`, the location pathname must begin with the portion of the URL pathname that was matched by all parent routes. The current pathname base is "${parentPathnameBase}" but pathname "${parsedLocationArg.pathname}" was given in the \`location\` prop.`);
		location = parsedLocationArg;
	} else location = locationFromContext;
	let pathname = location.pathname || "/";
	let remainingPathname = pathname;
	if (parentPathnameBase !== "/") {
		let parentSegments = parentPathnameBase.replace(/^\//, "").split("/");
		remainingPathname = "/" + pathname.replace(/^\//, "").split("/").slice(parentSegments.length).join("/");
	}
	let matches = dataRouterOpts && dataRouterOpts.state.matches.length ? dataRouterOpts.state.matches.map((m) => Object.assign(m, { route: dataRouterOpts.manifest[m.route.id] || m.route })) : matchRoutes(routes, { pathname: remainingPathname });
	let renderedMatches = _renderMatches(matches && matches.map((match) => Object.assign({}, match, {
		params: Object.assign({}, parentParams, match.params),
		pathname: joinPaths([parentPathnameBase, navigator.encodeLocation ? navigator.encodeLocation(match.pathname.replace(/%/g, "%25").replace(/\?/g, "%3F").replace(/#/g, "%23")).pathname : match.pathname]),
		pathnameBase: match.pathnameBase === "/" ? parentPathnameBase : joinPaths([parentPathnameBase, navigator.encodeLocation ? navigator.encodeLocation(match.pathnameBase.replace(/%/g, "%25").replace(/\?/g, "%3F").replace(/#/g, "%23")).pathname : match.pathnameBase])
	})), parentMatches, dataRouterOpts);
	if (locationArg && renderedMatches) return /* @__PURE__ */ import_react.createElement(LocationContext.Provider, { value: {
		location: {
			pathname: "/",
			search: "",
			hash: "",
			state: null,
			key: "default",
			mask: void 0,
			...location
		},
		navigationType: "POP"
	} }, renderedMatches);
	return renderedMatches;
}
function DefaultErrorComponent() {
	let error = useRouteError();
	let message = isRouteErrorResponse(error) ? `${error.status} ${error.statusText}` : error instanceof Error ? error.message : JSON.stringify(error);
	let stack = error instanceof Error ? error.stack : null;
	return /* @__PURE__ */ import_react.createElement(import_react.Fragment, null, /* @__PURE__ */ import_react.createElement("h2", null, "Unexpected Application Error!"), /* @__PURE__ */ import_react.createElement("h3", { style: { fontStyle: "italic" } }, message), stack ? /* @__PURE__ */ import_react.createElement("pre", { style: {
		padding: "0.5rem",
		backgroundColor: "rgba(200,200,200, 0.5)"
	} }, stack) : null, null);
}
var defaultErrorElement = /* @__PURE__ */ import_react.createElement(DefaultErrorComponent, null);
var RenderErrorBoundary = class extends import_react.Component {
	constructor(props) {
		super(props);
		this.state = {
			location: props.location,
			revalidation: props.revalidation,
			error: props.error
		};
	}
	static contextType = RSCRouterContext;
	static getDerivedStateFromError(error) {
		return { error };
	}
	static getDerivedStateFromProps(props, state) {
		if (state.location !== props.location || state.revalidation !== "idle" && props.revalidation === "idle") return {
			error: props.error,
			location: props.location,
			revalidation: props.revalidation
		};
		return {
			error: props.error !== void 0 ? props.error : state.error,
			location: state.location,
			revalidation: props.revalidation || state.revalidation
		};
	}
	componentDidCatch(error, errorInfo) {
		if (this.props.onError) this.props.onError(error, errorInfo);
		else console.error("React Router caught the following error during render", error);
	}
	render() {
		let error = this.state.error;
		if (this.context && typeof error === "object" && error && "digest" in error && typeof error.digest === "string") {
			const decoded = decodeRouteErrorResponseDigest(error.digest);
			if (decoded) error = decoded;
		}
		let result = error !== void 0 ? /* @__PURE__ */ import_react.createElement(RouteContext.Provider, { value: this.props.routeContext }, /* @__PURE__ */ import_react.createElement(RouteErrorContext.Provider, {
			value: error,
			children: this.props.component
		})) : this.props.children;
		if (this.context) return /* @__PURE__ */ import_react.createElement(RSCErrorHandler, { error }, result);
		return result;
	}
};
var errorRedirectHandledMap = /* @__PURE__ */ new WeakMap();
function RSCErrorHandler({ children, error }) {
	let { basename } = import_react.useContext(NavigationContext);
	if (typeof error === "object" && error && "digest" in error && typeof error.digest === "string") {
		let redirect = decodeRedirectErrorDigest(error.digest);
		if (redirect) {
			let existingRedirect = errorRedirectHandledMap.get(error);
			if (existingRedirect) throw existingRedirect;
			let parsed = parseToInfo(redirect.location, basename);
			let target = parsed.absoluteURL || parsed.to;
			if (hasInvalidProtocol(target)) throw new Error("Invalid redirect location");
			if (isBrowser$1 && !errorRedirectHandledMap.get(error)) if (parsed.isExternal || redirect.reloadDocument) window.location.href = target;
			else {
				const redirectPromise = Promise.resolve().then(() => window.__reactRouterDataRouter.navigate(parsed.to, { replace: redirect.replace }));
				errorRedirectHandledMap.set(error, redirectPromise);
				throw redirectPromise;
			}
			return /* @__PURE__ */ import_react.createElement("meta", {
				httpEquiv: "refresh",
				content: `0;url=${target}`
			});
		}
	}
	return children;
}
function RenderedRoute({ routeContext, match, children }) {
	let dataRouterContext = import_react.useContext(DataRouterContext);
	if (dataRouterContext && dataRouterContext.static && dataRouterContext.staticContext && (match.route.errorElement || match.route.ErrorBoundary)) dataRouterContext.staticContext._deepestRenderedBoundaryId = match.route.id;
	return /* @__PURE__ */ import_react.createElement(RouteContext.Provider, { value: routeContext }, children);
}
function _renderMatches(matches, parentMatches = [], dataRouterOpts) {
	let dataRouterState = dataRouterOpts?.state;
	if (matches == null) {
		if (!dataRouterState) return null;
		if (dataRouterState.errors) matches = dataRouterState.matches;
		else if (parentMatches.length === 0 && !dataRouterState.initialized && dataRouterState.matches.length > 0) matches = dataRouterState.matches;
		else return null;
	}
	let renderedMatches = matches;
	let errors = dataRouterState?.errors;
	if (errors != null) {
		let errorIndex = renderedMatches.findIndex((m) => m.route.id && errors?.[m.route.id] !== void 0);
		invariant$1(errorIndex >= 0, `Could not find a matching route for errors on route IDs: ${Object.keys(errors).join(",")}`);
		renderedMatches = renderedMatches.slice(0, Math.min(renderedMatches.length, errorIndex + 1));
	}
	let renderFallback = false;
	let fallbackIndex = -1;
	if (dataRouterOpts && dataRouterState) {
		renderFallback = dataRouterState.renderFallback;
		for (let i = 0; i < renderedMatches.length; i++) {
			let match = renderedMatches[i];
			if (match.route.HydrateFallback || match.route.hydrateFallbackElement) fallbackIndex = i;
			if (match.route.id) {
				let { loaderData, errors } = dataRouterState;
				let needsToRunLoader = match.route.loader && !loaderData.hasOwnProperty(match.route.id) && (!errors || errors[match.route.id] === void 0);
				if (match.route.lazy || needsToRunLoader) {
					if (dataRouterOpts.isStatic) renderFallback = true;
					if (fallbackIndex >= 0) renderedMatches = renderedMatches.slice(0, fallbackIndex + 1);
					else renderedMatches = [renderedMatches[0]];
					break;
				}
			}
		}
	}
	let onErrorHandler = dataRouterOpts?.onError;
	let onError = dataRouterState && onErrorHandler ? (error, errorInfo) => {
		onErrorHandler(error, {
			location: dataRouterState.location,
			params: dataRouterState.matches?.[0]?.params ?? {},
			pattern: getRoutePattern(dataRouterState.matches),
			errorInfo
		});
	} : void 0;
	return renderedMatches.reduceRight((outlet, match, index) => {
		let error;
		let shouldRenderHydrateFallback = false;
		let errorElement = null;
		let hydrateFallbackElement = null;
		if (dataRouterState) {
			error = errors && match.route.id ? errors[match.route.id] : void 0;
			errorElement = match.route.errorElement || defaultErrorElement;
			if (renderFallback) {
				if (fallbackIndex < 0 && index === 0) {
					warningOnce("route-fallback", false, "No `HydrateFallback` element provided to render during initial hydration");
					shouldRenderHydrateFallback = true;
					hydrateFallbackElement = null;
				} else if (fallbackIndex === index) {
					shouldRenderHydrateFallback = true;
					hydrateFallbackElement = match.route.hydrateFallbackElement || null;
				}
			}
		}
		let matches = parentMatches.concat(renderedMatches.slice(0, index + 1));
		let getChildren = () => {
			let children;
			if (error) children = errorElement;
			else if (shouldRenderHydrateFallback) children = hydrateFallbackElement;
			else if (match.route.Component) children = /* @__PURE__ */ import_react.createElement(match.route.Component, null);
			else if (match.route.element) children = match.route.element;
			else children = outlet;
			return /* @__PURE__ */ import_react.createElement(RenderedRoute, {
				match,
				routeContext: {
					outlet,
					matches,
					isDataRoute: dataRouterState != null
				},
				children
			});
		};
		return dataRouterState && (match.route.ErrorBoundary || match.route.errorElement || index === 0) ? /* @__PURE__ */ import_react.createElement(RenderErrorBoundary, {
			location: dataRouterState.location,
			revalidation: dataRouterState.revalidation,
			component: errorElement,
			error,
			children: getChildren(),
			routeContext: {
				outlet: null,
				matches,
				isDataRoute: true
			},
			onError
		}) : getChildren();
	}, null);
}
function getDataRouterConsoleError$1(hookName) {
	return `${hookName} must be used within a data router.  See https://reactrouter.com/en/main/routers/picking-a-router.`;
}
function useDataRouterContext$2(hookName) {
	let ctx = import_react.useContext(DataRouterContext);
	invariant$1(ctx, getDataRouterConsoleError$1(hookName));
	return ctx;
}
function useDataRouterState$1(hookName) {
	let state = import_react.useContext(DataRouterStateContext);
	invariant$1(state, getDataRouterConsoleError$1(hookName));
	return state;
}
function useRouteContext(hookName) {
	let route = import_react.useContext(RouteContext);
	invariant$1(route, getDataRouterConsoleError$1(hookName));
	return route;
}
function useCurrentRouteId(hookName) {
	let route = useRouteContext(hookName);
	let thisRoute = route.matches[route.matches.length - 1];
	invariant$1(thisRoute.route.id, `${hookName} can only be used on routes that contain a unique "id"`);
	return thisRoute.route.id;
}
/**
* Returns the ID for the nearest contextual route
*
* @category Hooks
* @returns The ID of the nearest contextual route
*/
function useRouteId() {
	return useCurrentRouteId("useRouteId");
}
/**
* Returns the current {@link Navigation}, defaulting to an "idle" navigation
* when no navigation is in progress. You can use this to render pending UI
* (like a global spinner) or read [`FormData`](https://developer.mozilla.org/en-US/docs/Web/API/FormData)
* from a form navigation.
*
* @example
* import { useNavigation } from "react-router";
*
* function SomeComponent() {
*   let navigation = useNavigation();
*   navigation.state;
*   navigation.formData;
*   // etc.
* }
*
* @public
* @category Hooks
* @mode framework
* @mode data
* @returns The current {@link Navigation} object
*/
function useNavigation() {
	let state = useDataRouterState$1("useNavigation");
	return import_react.useMemo(() => {
		let { matches, historyAction, ...rest } = state.navigation;
		return rest;
	}, [state.navigation]);
}
/**
* Returns the active route matches, useful for accessing `loaderData` for
* parent/child routes or the route [`handle`](../../start/framework/route-module#handle)
* property
*
* Pairing the route `handle` with `useMatches` gets very powerful since you can put
* whatever you want on a route handle and have access to `useMatches` anywhere.
* Please see the [handle](../../how-to/using-handle) documentation for an example
* of breadcrumbs via `useMatches`/`handle`.
*
* ```tsx
* import { useMatches } from "react-router";
*
* function SomeComponent() {
*   const matches = useMatches();
*   // matches[i].id          // route id
*   // matches[i].pathname    // the portion of the URL the route matched
*   // matches[i].params      // the parsed params from the URL
*   // matches[i].loaderData  // the data from the loader
*   // matches[i].handle      // the route handle with any app specific data
* }
* ```
*
* <docs-info>useMatches only works with a data router like `createBrowserRouter`,
* since they know the full route tree up front and can provide all of the current
* matches. Additionally, `useMatches` will not match down into any descendant route
* trees since the router isn't aware of the descendant routes.</docs-info>
*
* @public
* @category Hooks
* @mode framework
* @mode data
* @returns An array of {@link UIMatch | UI matches} for the current route hierarchy
*/
function useMatches() {
	let { matches, loaderData } = useDataRouterState$1("useMatches");
	return import_react.useMemo(() => matches.map((m) => convertRouteMatchToUiMatch(m, loaderData)), [matches, loaderData]);
}
/**
* Returns the data from the closest route
* [`loader`](../../start/framework/route-module#loader) or
* [`clientLoader`](../../start/framework/route-module#clientloader).
*
* @example
* import { useLoaderData } from "react-router";
*
* export async function loader() {
*   return await fakeDb.invoices.findAll();
* }
*
* export default function Invoices() {
*   let invoices = useLoaderData<typeof loader>();
*   // ...
* }
*
* @public
* @category Hooks
* @mode framework
* @mode data
* @returns The data returned from the route's [`loader`](../../start/framework/route-module#loader) or [`clientLoader`](../../start/framework/route-module#clientloader) function
*/
function useLoaderData() {
	let state = useDataRouterState$1("useLoaderData");
	let routeId = useCurrentRouteId("useLoaderData");
	return state.loaderData[routeId];
}
/**
* Returns the [`action`](../../start/framework/route-module#action) data from
* the most recent `POST` navigation form submission or `undefined` if there
* hasn't been one.
*
* @example
* import { Form, useActionData } from "react-router";
*
* export async function action({ request }) {
*   const body = await request.formData();
*   const name = body.get("visitorsName");
*   return { message: `Hello, ${name}` };
* }
*
* export default function Invoices() {
*   const data = useActionData();
*   return (
*     <Form method="post">
*       <input type="text" name="visitorsName" />
*       {data ? data.message : "Waiting..."}
*     </Form>
*   );
* }
*
* @public
* @category Hooks
* @mode framework
* @mode data
* @returns The data returned from the route's [`action`](../../start/framework/route-module#action)
* function, or `undefined` if no [`action`](../../start/framework/route-module#action)
* has been called
*/
function useActionData() {
	let state = useDataRouterState$1("useActionData");
	let routeId = useCurrentRouteId("useLoaderData");
	return state.actionData ? state.actionData[routeId] : void 0;
}
/**
* Accesses the error thrown during an
* [`action`](../../start/framework/route-module#action),
* [`loader`](../../start/framework/route-module#loader),
* or component render to be used in a route module
* [`ErrorBoundary`](../../start/framework/route-module#errorboundary).
*
* @example
* export function ErrorBoundary() {
*   const error = useRouteError();
*   return <div>{error.message}</div>;
* }
*
* @public
* @category Hooks
* @mode framework
* @mode data
* @returns The error that was thrown during route [loading](../../start/framework/route-module#loader),
* [`action`](../../start/framework/route-module#action) execution, or rendering
*/
function useRouteError() {
	let error = import_react.useContext(RouteErrorContext);
	let state = useDataRouterState$1("useRouteError");
	let routeId = useCurrentRouteId("useRouteError");
	if (error !== void 0) return error;
	return state.errors?.[routeId];
}
function useNavigateStable() {
	let { router } = useDataRouterContext$2("useNavigate");
	let id = useCurrentRouteId("useNavigate");
	let activeRef = import_react.useRef(false);
	import_react.useLayoutEffect(() => {
		activeRef.current = true;
	});
	return import_react.useCallback(async (to, options = {}) => {
		warning(activeRef.current, navigateEffectWarning);
		if (!activeRef.current) return;
		if (typeof to === "number") await router.navigate(to);
		else await router.navigate(to, {
			fromRouteId: id,
			...options
		});
	}, [router, id]);
}
var alreadyWarned$1 = {};
function warningOnce(key, cond, message) {
	if (!cond && !alreadyWarned$1[key]) {
		alreadyWarned$1[key] = true;
		warning(false, message);
	}
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/warnings.js
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
var alreadyWarned = {};
function warnOnce(condition, message) {
	if (!condition && !alreadyWarned[message]) {
		alreadyWarned[message] = true;
		console.warn(message);
	}
}
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
import_react.memo(DataRoutes);
function DataRoutes({ routes, manifest, future, state, isStatic, onError }) {
	return useRoutesImpl(routes, void 0, {
		manifest,
		state,
		isStatic,
		onError,
		future
	});
}
/**
* Renders the matching child route of a parent route or nothing if no child
* route matches.
*
* @example
* import { Outlet } from "react-router";
*
* export default function SomeParent() {
*   return (
*     <div>
*       <h1>Parent Content</h1>
*       <Outlet />
*     </div>
*   );
* }
*
* @public
* @category Components
* @param props Props
* @param {OutletProps.context} props.context n/a
* @returns React element for the rendered outlet or `null` if no child route matches.
*/
function Outlet(props) {
	return useOutlet(props.context);
}
/**
* Provides location context for the rest of the app.
*
* Note: You usually won't render a `<Router>` directly. Instead, you'll render a
* router that is more specific to your environment such as a {@link BrowserRouter}
* in web browsers or a {@link ServerRouter} for server rendering.
*
* @public
* @category Declarative Routers
* @mode declarative
* @param props Props
* @param {RouterProps.basename} props.basename n/a
* @param {RouterProps.children} props.children n/a
* @param {RouterProps.location} props.location n/a
* @param {RouterProps.navigationType} props.navigationType n/a
* @param {RouterProps.navigator} props.navigator n/a
* @param {RouterProps.static} props.static n/a
* @param {RouterProps.useTransitions} props.useTransitions n/a
* @returns React element for the rendered router or `null` if the location does
* not match the {@link props.basename}
*/
function Router({ basename: basenameProp = "/", children = null, location: locationProp, navigationType = "POP", navigator, static: staticProp = false, useTransitions }) {
	invariant$1(!useInRouterContext(), "You cannot render a <Router> inside another <Router>. You should never have more than one in your app.");
	let basename = basenameProp.replace(/^\/*/, "/");
	let navigationContext = import_react.useMemo(() => ({
		basename,
		navigator,
		static: staticProp,
		useTransitions,
		future: {}
	}), [
		basename,
		navigator,
		staticProp,
		useTransitions
	]);
	if (typeof locationProp === "string") locationProp = parsePath(locationProp);
	let { pathname = "/", search = "", hash = "", state = null, key = "default", mask } = locationProp;
	let locationContext = import_react.useMemo(() => {
		let trailingPathname = stripBasename(pathname, basename);
		if (trailingPathname == null) return null;
		return {
			location: {
				pathname: trailingPathname,
				search,
				hash,
				state,
				key,
				mask
			},
			navigationType
		};
	}, [
		basename,
		pathname,
		search,
		hash,
		state,
		key,
		navigationType,
		mask
	]);
	warning(locationContext != null, `<Router basename="${basename}"> is not able to match the URL "${pathname}${search}${hash}" because it does not start with the basename, so the <Router> won't render anything.`);
	if (locationContext == null) return null;
	return /* @__PURE__ */ import_react.createElement(NavigationContext.Provider, { value: navigationContext }, /* @__PURE__ */ import_react.createElement(LocationContext.Provider, {
		children,
		value: locationContext
	}));
}
import_react.Component;
function useRouteComponentProps() {
	return {
		params: useParams(),
		loaderData: useLoaderData(),
		actionData: useActionData(),
		matches: useMatches()
	};
}
function withComponentProps(Component) {
	return function WithComponentProps() {
		const props = useRouteComponentProps();
		return import_react.createElement(Component, props);
	};
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/dom/dom.js
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
var defaultEncType = "application/x-www-form-urlencoded";
function isHtmlElement(object) {
	return typeof HTMLElement !== "undefined" && object instanceof HTMLElement;
}
function isButtonElement(object) {
	return isHtmlElement(object) && object.tagName.toLowerCase() === "button";
}
function isFormElement(object) {
	return isHtmlElement(object) && object.tagName.toLowerCase() === "form";
}
function isInputElement(object) {
	return isHtmlElement(object) && object.tagName.toLowerCase() === "input";
}
function isModifiedEvent(event) {
	return !!(event.metaKey || event.altKey || event.ctrlKey || event.shiftKey);
}
function shouldProcessLinkClick(event, target) {
	return event.button === 0 && (!target || target === "_self") && !isModifiedEvent(event);
}
var _formDataSupportsSubmitter = null;
function isFormDataSubmitterSupported() {
	if (_formDataSupportsSubmitter === null) try {
		new FormData(document.createElement("form"), 0);
		_formDataSupportsSubmitter = false;
	} catch (e) {
		_formDataSupportsSubmitter = true;
	}
	return _formDataSupportsSubmitter;
}
var supportedFormEncTypes = /* @__PURE__ */ new Set([
	"application/x-www-form-urlencoded",
	"multipart/form-data",
	"text/plain"
]);
function getFormEncType(encType) {
	if (encType != null && !supportedFormEncTypes.has(encType)) {
		warning(false, `"${encType}" is not a valid \`encType\` for \`<Form>\`/\`<fetcher.Form>\` and will default to "${defaultEncType}"`);
		return null;
	}
	return encType;
}
function getFormSubmissionInfo(target, basename) {
	let method;
	let action;
	let encType;
	let formData;
	let body;
	if (isFormElement(target)) {
		let attr = target.getAttribute("action");
		action = attr ? stripBasename(attr, basename) : null;
		method = target.getAttribute("method") || "get";
		encType = getFormEncType(target.getAttribute("enctype")) || defaultEncType;
		formData = new FormData(target);
	} else if (isButtonElement(target) || isInputElement(target) && (target.type === "submit" || target.type === "image")) {
		let form = target.form;
		if (form == null) throw new Error(`Cannot submit a <button> or <input type="submit"> without a <form>`);
		let attr = target.getAttribute("formaction") || form.getAttribute("action");
		action = attr ? stripBasename(attr, basename) : null;
		method = target.getAttribute("formmethod") || form.getAttribute("method") || "get";
		encType = getFormEncType(target.getAttribute("formenctype")) || getFormEncType(form.getAttribute("enctype")) || defaultEncType;
		formData = new FormData(form, target);
		if (!isFormDataSubmitterSupported()) {
			let { name, type, value } = target;
			if (type === "image") {
				let prefix = name ? `${name}.` : "";
				formData.append(`${prefix}x`, "0");
				formData.append(`${prefix}y`, "0");
			} else if (name) formData.append(name, value);
		}
	} else if (isHtmlElement(target)) throw new Error("Cannot submit element that is not <form>, <button>, or <input type=\"submit|image\">");
	else {
		method = "get";
		action = null;
		encType = defaultEncType;
		body = target;
	}
	if (formData && encType === "text/plain") {
		body = formData;
		formData = void 0;
	}
	return {
		action,
		method: method.toLowerCase(),
		encType,
		formData,
		body
	};
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/dom/ssr/invariant.js
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
	if (value === false || value === null || typeof value === "undefined") throw new Error(message);
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/dom/ssr/routeModules.js
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
async function loadRouteModule(route, routeModulesCache) {
	if (route.id in routeModulesCache) return routeModulesCache[route.id];
	try {
		let routeModule = await import(
			/* @vite-ignore */
			/* webpackIgnore: true */
			route.module
);
		routeModulesCache[route.id] = routeModule;
		return routeModule;
	} catch (error) {
		console.error(`Error loading route module \`${route.module}\`, reloading page...`);
		console.error(error);
		if (window.__reactRouterContext && window.__reactRouterContext.isSpaMode && void 0);
		window.location.reload();
		return new Promise(() => {});
	}
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/dom/ssr/links.js
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
* Gets all the links for a set of matches. The modules are assumed to have been
* loaded already.
*/
function getKeyedLinksForMatches(matches, routeModules, manifest) {
	return dedupeLinkDescriptors(matches.map((match) => {
		let module = routeModules[match.route.id];
		let route = manifest.routes[match.route.id];
		return [route && route.css ? route.css.map((href) => ({
			rel: "stylesheet",
			href
		})) : [], module?.links?.() || []];
	}).flat(2), getModuleLinkHrefs(matches, manifest));
}
function isPageLinkDescriptor(object) {
	return object != null && typeof object.page === "string";
}
function isHtmlLinkDescriptor(object) {
	if (object == null) return false;
	if (object.href == null) return object.rel === "preload" && typeof object.imageSrcSet === "string" && typeof object.imageSizes === "string";
	return typeof object.rel === "string" && typeof object.href === "string";
}
async function getKeyedPrefetchLinks(matches, manifest, routeModules) {
	return dedupeLinkDescriptors((await Promise.all(matches.map(async (match) => {
		let route = manifest.routes[match.route.id];
		if (route) {
			let mod = await loadRouteModule(route, routeModules);
			return mod.links ? mod.links() : [];
		}
		return [];
	}))).flat(1).filter(isHtmlLinkDescriptor).filter((link) => link.rel === "stylesheet" || link.rel === "preload").map((link) => link.rel === "stylesheet" ? {
		...link,
		rel: "prefetch",
		as: "style"
	} : {
		...link,
		rel: "prefetch"
	}));
}
function getNewMatchesForLinks(page, nextMatches, currentMatches, manifest, location, mode) {
	let isNew = (match, index) => {
		if (!currentMatches[index]) return true;
		return match.route.id !== currentMatches[index].route.id;
	};
	let matchPathChanged = (match, index) => {
		return currentMatches[index].pathname !== match.pathname || currentMatches[index].route.path?.endsWith("*") && currentMatches[index].params["*"] !== match.params["*"];
	};
	if (mode === "assets") return nextMatches.filter((match, index) => isNew(match, index) || matchPathChanged(match, index));
	if (mode === "data") return nextMatches.filter((match, index) => {
		let manifestRoute = manifest.routes[match.route.id];
		if (!manifestRoute || !manifestRoute.hasLoader) return false;
		if (isNew(match, index) || matchPathChanged(match, index)) return true;
		if (match.route.shouldRevalidate) {
			let routeChoice = match.route.shouldRevalidate({
				currentUrl: new URL(location.pathname + location.search + location.hash, window.origin),
				currentParams: currentMatches[0]?.params || {},
				nextUrl: new URL(page, window.origin),
				nextParams: match.params,
				defaultShouldRevalidate: true
			});
			if (typeof routeChoice === "boolean") return routeChoice;
		}
		return true;
	});
	return [];
}
function getModuleLinkHrefs(matches, manifest, { includeHydrateFallback } = {}) {
	return dedupeHrefs(matches.map((match) => {
		let route = manifest.routes[match.route.id];
		if (!route) return [];
		let hrefs = [route.module];
		if (route.clientActionModule) hrefs = hrefs.concat(route.clientActionModule);
		if (route.clientLoaderModule) hrefs = hrefs.concat(route.clientLoaderModule);
		if (includeHydrateFallback && route.hydrateFallbackModule) hrefs = hrefs.concat(route.hydrateFallbackModule);
		if (route.imports) hrefs = hrefs.concat(route.imports);
		return hrefs;
	}).flat(1));
}
function dedupeHrefs(hrefs) {
	return [...new Set(hrefs)];
}
function sortKeys(obj) {
	let sorted = {};
	let keys = Object.keys(obj).sort();
	for (let key of keys) sorted[key] = obj[key];
	return sorted;
}
function dedupeLinkDescriptors(descriptors, preloads) {
	let set = /* @__PURE__ */ new Set();
	let preloadsSet = new Set(preloads);
	return descriptors.reduce((deduped, descriptor) => {
		if (preloads && !isPageLinkDescriptor(descriptor) && descriptor.as === "script" && descriptor.href && preloadsSet.has(descriptor.href)) return deduped;
		let key = JSON.stringify(sortKeys(descriptor));
		if (!set.has(key)) {
			set.add(key);
			deduped.push({
				key,
				link: descriptor
			});
		}
		return deduped;
	}, []);
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/dom/ssr/fallback.js
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
function RemixRootDefaultHydrateFallback() {
	let { nonce } = useFrameworkContext();
	return /* @__PURE__ */ import_react.createElement(BoundaryShell, {
		title: "Loading...",
		renderScripts: true
	}, null);
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/dom/ssr/routes.js
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
function getRouteComponents(route, routeModule, isSpaMode) {
	let Component = getRouteModuleComponent(routeModule);
	let HydrateFallback = routeModule.HydrateFallback && (!isSpaMode || route.id === "root") ? routeModule.HydrateFallback : route.id === "root" ? RemixRootDefaultHydrateFallback : void 0;
	let ErrorBoundary = routeModule.ErrorBoundary ? routeModule.ErrorBoundary : route.id === "root" ? () => /* @__PURE__ */ import_react.createElement(RemixRootDefaultErrorBoundary, { error: useRouteError() }) : void 0;
	if (route.id === "root" && routeModule.Layout) return {
		...Component ? { element: /* @__PURE__ */ import_react.createElement(routeModule.Layout, null, /* @__PURE__ */ import_react.createElement(Component, null)) } : { Component },
		...ErrorBoundary ? { errorElement: /* @__PURE__ */ import_react.createElement(routeModule.Layout, null, /* @__PURE__ */ import_react.createElement(ErrorBoundary, null)) } : { ErrorBoundary },
		...HydrateFallback ? { hydrateFallbackElement: /* @__PURE__ */ import_react.createElement(routeModule.Layout, null, /* @__PURE__ */ import_react.createElement(HydrateFallback, null)) } : { HydrateFallback }
	};
	return {
		Component,
		ErrorBoundary,
		HydrateFallback
	};
}
function createServerRoutes(manifest, routeModules, future, isSpaMode, parentId = "", routesByParentId = groupRoutesByParentId(manifest), spaModeLazyPromise = Promise.resolve({ Component: () => null })) {
	return (routesByParentId[parentId] || []).map((route) => {
		let routeModule = routeModules[route.id];
		invariant(routeModule, "No `routeModule` available to create server routes");
		let dataRoute = {
			...getRouteComponents(route, routeModule, isSpaMode),
			caseSensitive: route.caseSensitive,
			id: route.id,
			index: route.index,
			path: route.path,
			handle: routeModule.handle,
			lazy: isSpaMode ? () => spaModeLazyPromise : void 0,
			loader: route.hasLoader || route.hasClientLoader ? () => null : void 0
		};
		let children = createServerRoutes(manifest, routeModules, future, isSpaMode, route.id, routesByParentId, spaModeLazyPromise);
		if (children.length > 0) dataRoute.children = children;
		return dataRoute;
	});
}
function getRouteModuleComponent(routeModule) {
	if (routeModule.default == null) return void 0;
	if (!(typeof routeModule.default === "object" && Object.keys(routeModule.default).length === 0)) return routeModule.default;
}
function shouldHydrateRouteLoader(routeId, clientLoader, hasLoader, isSpaMode) {
	return isSpaMode && routeId !== "root" || clientLoader != null && (clientLoader.hydrate === true || hasLoader !== true);
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/dom/ssr/components.js
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
function useDataRouterContext$1() {
	let context = import_react.useContext(DataRouterContext);
	invariant(context, "You must render this element inside a <DataRouterContext.Provider> element");
	return context;
}
function useDataRouterStateContext() {
	let context = import_react.useContext(DataRouterStateContext);
	invariant(context, "You must render this element inside a <DataRouterStateContext.Provider> element");
	return context;
}
var FrameworkContext = import_react.createContext(void 0);
FrameworkContext.displayName = "FrameworkContext";
function useFrameworkContext() {
	let context = import_react.useContext(FrameworkContext);
	invariant(context, "You must render this element inside a <HydratedRouter> element");
	return context;
}
function usePrefetchBehavior(prefetch, theirElementProps) {
	let frameworkContext = import_react.useContext(FrameworkContext);
	let [maybePrefetch, setMaybePrefetch] = import_react.useState(false);
	let [shouldPrefetch, setShouldPrefetch] = import_react.useState(false);
	let { onFocus, onBlur, onMouseEnter, onMouseLeave, onTouchStart } = theirElementProps;
	let ref = import_react.useRef(null);
	import_react.useEffect(() => {
		if (prefetch === "render") setShouldPrefetch(true);
		if (prefetch === "viewport") {
			let callback = (entries) => {
				entries.forEach((entry) => {
					setShouldPrefetch(entry.isIntersecting);
				});
			};
			let observer = new IntersectionObserver(callback, { threshold: .5 });
			if (ref.current) observer.observe(ref.current);
			return () => {
				observer.disconnect();
			};
		}
	}, [prefetch]);
	import_react.useEffect(() => {
		if (maybePrefetch) {
			let id = setTimeout(() => {
				setShouldPrefetch(true);
			}, 100);
			return () => {
				clearTimeout(id);
			};
		}
	}, [maybePrefetch]);
	let setIntent = () => {
		setMaybePrefetch(true);
	};
	let cancelIntent = () => {
		setMaybePrefetch(false);
		setShouldPrefetch(false);
	};
	if (!frameworkContext) return [
		false,
		ref,
		{}
	];
	if (prefetch !== "intent") return [
		shouldPrefetch,
		ref,
		{}
	];
	return [
		shouldPrefetch,
		ref,
		{
			onFocus: composeEventHandlers(onFocus, setIntent),
			onBlur: composeEventHandlers(onBlur, cancelIntent),
			onMouseEnter: composeEventHandlers(onMouseEnter, setIntent),
			onMouseLeave: composeEventHandlers(onMouseLeave, cancelIntent),
			onTouchStart: composeEventHandlers(onTouchStart, setIntent)
		}
	];
}
function composeEventHandlers(theirHandler, ourHandler) {
	return (event) => {
		theirHandler && theirHandler(event);
		if (!event.defaultPrevented) ourHandler(event);
	};
}
function getActiveMatches(matches, errors, isSpaMode) {
	if (isSpaMode && !isHydrated) return [matches[0]];
	if (errors) {
		let errorIdx = matches.findIndex((m) => errors[m.route.id] !== void 0);
		return matches.slice(0, errorIdx + 1);
	}
	return matches;
}
var CRITICAL_CSS_DATA_ATTRIBUTE = "data-react-router-critical-css";
/**
* Renders all the [`<link>`](https://developer.mozilla.org/en-US/docs/Web/HTML/Element/link)
* tags created by the route module's [`links`](../../start/framework/route-module#links)
* export. You should render it inside the [`<head>`](https://developer.mozilla.org/en-US/docs/Web/HTML/Element/head)
* of your document.
*
* @example
* import { Links } from "react-router";
*
* export default function Root() {
*   return (
*     <html>
*       <head>
*         <Links />
*       </head>
*       <body></body>
*     </html>
*   );
* }
*
* @public
* @category Components
* @mode framework
* @param props Props
* @param {LinksProps.nonce} props.nonce n/a
* @param {LinksProps.crossOrigin} props.crossOrigin n/a
* @returns A collection of React elements for [`<link>`](https://developer.mozilla.org/en-US/docs/Web/HTML/Element/link)
* tags
*/
function Links({ nonce, crossOrigin }) {
	let { isSpaMode, manifest, routeModules, criticalCss, nonce: contextNonce } = useFrameworkContext();
	let { errors, matches: routerMatches } = useDataRouterStateContext();
	let matches = getActiveMatches(routerMatches, errors, isSpaMode);
	let keyedLinks = import_react.useMemo(() => getKeyedLinksForMatches(matches, routeModules, manifest), [
		matches,
		routeModules,
		manifest
	]);
	if (nonce == null && contextNonce) nonce = contextNonce;
	return /* @__PURE__ */ import_react.createElement(import_react.Fragment, null, typeof criticalCss === "string" ? /* @__PURE__ */ import_react.createElement("style", {
		[CRITICAL_CSS_DATA_ATTRIBUTE]: "",
		nonce,
		dangerouslySetInnerHTML: { __html: criticalCss }
	}) : null, typeof criticalCss === "object" ? /* @__PURE__ */ import_react.createElement("link", {
		[CRITICAL_CSS_DATA_ATTRIBUTE]: "",
		rel: "stylesheet",
		href: criticalCss.href,
		nonce,
		crossOrigin
	}) : null, keyedLinks.map(({ key, link }) => isPageLinkDescriptor(link) ? /* @__PURE__ */ import_react.createElement(PrefetchPageLinks, {
		key,
		nonce,
		...link,
		crossOrigin: link.crossOrigin ?? crossOrigin
	}) : /* @__PURE__ */ import_react.createElement("link", {
		key,
		nonce,
		...link,
		crossOrigin: link.crossOrigin ?? crossOrigin
	})));
}
/**
* Renders [`<link rel=prefetch|modulepreload>`](https://developer.mozilla.org/en-US/docs/Web/API/HTMLLinkElement/rel)
* tags for modules and data of another page to enable an instant navigation to
* that page. [`<Link prefetch>`](./Link#prefetch) uses this internally, but you
* can render it to prefetch a page for any other reason.
*
* For example, you may render one of this as the user types into a search field
* to prefetch search results before they click through to their selection.
*
* @example
* import { PrefetchPageLinks } from "react-router";
*
* <PrefetchPageLinks page="/absolute/path" />
*
* @public
* @category Components
* @mode framework
* @param props Props
* @param {PageLinkDescriptor.page} props.page n/a
* @param props.linkProps Additional props to spread onto the [`<link>`](https://developer.mozilla.org/en-US/docs/Web/HTML/Element/link)
* tags, such as [`crossOrigin`](https://developer.mozilla.org/en-US/docs/Web/API/HTMLLinkElement/crossOrigin),
* [`integrity`](https://developer.mozilla.org/en-US/docs/Web/API/HTMLLinkElement/integrity),
* [`rel`](https://developer.mozilla.org/en-US/docs/Web/API/HTMLLinkElement/rel),
* etc.
* @returns A collection of React elements for [`<link>`](https://developer.mozilla.org/en-US/docs/Web/HTML/Element/link)
* tags
*/
function PrefetchPageLinks({ page, ...linkProps }) {
	let rsc = useIsRSCRouterContext();
	let { nonce: contextNonce } = useFrameworkContext();
	let { router } = useDataRouterContext$1();
	let matches = import_react.useMemo(() => matchRoutes(router.routes, page, router.basename), [
		router.routes,
		page,
		router.basename
	]);
	if (!matches) return null;
	if (linkProps.nonce == null && contextNonce) linkProps = {
		...linkProps,
		nonce: contextNonce
	};
	if (rsc) return /* @__PURE__ */ import_react.createElement(RSCPrefetchPageLinksImpl, {
		page,
		matches,
		...linkProps
	});
	return /* @__PURE__ */ import_react.createElement(PrefetchPageLinksImpl, {
		page,
		matches,
		...linkProps
	});
}
function useKeyedPrefetchLinks(matches) {
	let { manifest, routeModules } = useFrameworkContext();
	let [keyedPrefetchLinks, setKeyedPrefetchLinks] = import_react.useState([]);
	import_react.useEffect(() => {
		let interrupted = false;
		getKeyedPrefetchLinks(matches, manifest, routeModules).then((links) => {
			if (!interrupted) setKeyedPrefetchLinks(links);
		});
		return () => {
			interrupted = true;
		};
	}, [
		matches,
		manifest,
		routeModules
	]);
	return keyedPrefetchLinks;
}
function RSCPrefetchPageLinksImpl({ page, matches: nextMatches, ...linkProps }) {
	let location = useLocation();
	let dataHrefs = import_react.useMemo(() => {
		if (page === location.pathname + location.search + location.hash) return [];
		let url = singleFetchUrl(page, "rsc");
		let hasSomeRoutesWithShouldRevalidate = false;
		let targetRoutes = [];
		for (let match of nextMatches) if (typeof match.route.shouldRevalidate === "function") hasSomeRoutesWithShouldRevalidate = true;
		else targetRoutes.push(match.route.id);
		if (hasSomeRoutesWithShouldRevalidate && targetRoutes.length > 0) url.searchParams.set("_routes", targetRoutes.join(","));
		return [url.pathname + url.search];
	}, [
		page,
		location,
		nextMatches
	]);
	return /* @__PURE__ */ import_react.createElement(import_react.Fragment, null, dataHrefs.map((href) => /* @__PURE__ */ import_react.createElement("link", {
		key: href,
		rel: "prefetch",
		as: "fetch",
		href,
		...linkProps
	})));
}
function PrefetchPageLinksImpl({ page, matches: nextMatches, ...linkProps }) {
	let location = useLocation();
	let { manifest, routeModules } = useFrameworkContext();
	let { loaderData, matches } = useDataRouterStateContext();
	let newMatchesForData = import_react.useMemo(() => getNewMatchesForLinks(page, nextMatches, matches, manifest, location, "data"), [
		page,
		nextMatches,
		matches,
		manifest,
		location
	]);
	let newMatchesForAssets = import_react.useMemo(() => getNewMatchesForLinks(page, nextMatches, matches, manifest, location, "assets"), [
		page,
		nextMatches,
		matches,
		manifest,
		location
	]);
	let dataHrefs = import_react.useMemo(() => {
		if (page === location.pathname + location.search + location.hash) return [];
		let routesParams = /* @__PURE__ */ new Set();
		let foundOptOutRoute = false;
		nextMatches.forEach((m) => {
			let manifestRoute = manifest.routes[m.route.id];
			if (!manifestRoute || !manifestRoute.hasLoader) return;
			if (!newMatchesForData.some((m2) => m2.route.id === m.route.id) && m.route.id in loaderData && routeModules[m.route.id]?.shouldRevalidate) foundOptOutRoute = true;
			else if (manifestRoute.hasClientLoader) foundOptOutRoute = true;
			else routesParams.add(m.route.id);
		});
		if (routesParams.size === 0) return [];
		let url = singleFetchUrl(page, "data");
		if (foundOptOutRoute && routesParams.size > 0) url.searchParams.set("_routes", nextMatches.filter((m) => routesParams.has(m.route.id)).map((m) => m.route.id).join(","));
		return [url.pathname + url.search];
	}, [
		loaderData,
		location,
		manifest,
		newMatchesForData,
		nextMatches,
		page,
		routeModules
	]);
	let moduleHrefs = import_react.useMemo(() => getModuleLinkHrefs(newMatchesForAssets, manifest), [newMatchesForAssets, manifest]);
	let keyedPrefetchLinks = useKeyedPrefetchLinks(newMatchesForAssets);
	return /* @__PURE__ */ import_react.createElement(import_react.Fragment, null, dataHrefs.map((href) => /* @__PURE__ */ import_react.createElement("link", {
		key: href,
		rel: "prefetch",
		as: "fetch",
		href,
		...linkProps
	})), moduleHrefs.map((href) => /* @__PURE__ */ import_react.createElement("link", {
		key: href,
		rel: "modulepreload",
		href,
		...linkProps
	})), keyedPrefetchLinks.map(({ key, link }) => /* @__PURE__ */ import_react.createElement("link", {
		key,
		nonce: linkProps.nonce,
		...link,
		crossOrigin: link.crossOrigin ?? linkProps.crossOrigin
	})));
}
/**
* Renders all the [`<meta>`](https://developer.mozilla.org/en-US/docs/Web/HTML/Element/meta)
* tags created by the route module's [`meta`](../../start/framework/route-module#meta)
* export. You should render it inside the [`<head>`](https://developer.mozilla.org/en-US/docs/Web/HTML/Element/head)
* of your document.
*
* @example
* import { Meta } from "react-router";
*
* export default function Root() {
*   return (
*     <html>
*       <head>
*         <Meta />
*       </head>
*     </html>
*   );
* }
*
* @public
* @category Components
* @mode framework
* @returns A collection of React elements for [`<meta>`](https://developer.mozilla.org/en-US/docs/Web/HTML/Element/meta)
* tags
*/
function Meta() {
	let { isSpaMode, routeModules } = useFrameworkContext();
	let { errors, matches: routerMatches, loaderData } = useDataRouterStateContext();
	let location = useLocation();
	let _matches = getActiveMatches(routerMatches, errors, isSpaMode);
	let error = null;
	if (errors) error = errors[_matches[_matches.length - 1].route.id];
	let meta = [];
	let leafMeta = null;
	let matches = [];
	for (let i = 0; i < _matches.length; i++) {
		let _match = _matches[i];
		let routeId = _match.route.id;
		let data = loaderData[routeId];
		let params = _match.params;
		let routeModule = routeModules[routeId];
		let routeMeta = [];
		let match = {
			id: routeId,
			loaderData: data,
			meta: [],
			params: _match.params,
			pathname: _match.pathname,
			handle: _match.route.handle,
			error
		};
		matches[i] = match;
		if (routeModule?.meta) routeMeta = typeof routeModule.meta === "function" ? routeModule.meta({
			loaderData: data,
			params,
			location,
			matches,
			error
		}) : Array.isArray(routeModule.meta) ? [...routeModule.meta] : routeModule.meta;
		else if (leafMeta) routeMeta = [...leafMeta];
		routeMeta = routeMeta || [];
		if (!Array.isArray(routeMeta)) throw new Error("The route at " + _match.route.path + " returns an invalid value. All route meta functions must return an array of meta objects.\n\nTo reference the meta function API, see https://reactrouter.com/start/framework/route-module#meta");
		match.meta = routeMeta;
		matches[i] = match;
		meta = [...routeMeta];
		leafMeta = meta;
	}
	return /* @__PURE__ */ import_react.createElement(import_react.Fragment, null, meta.flat().map((metaProps) => {
		if (!metaProps) return null;
		if ("tagName" in metaProps) {
			let { tagName, ...rest } = metaProps;
			if (!isValidMetaTag(tagName)) {
				console.warn(`A meta object uses an invalid tagName: ${tagName}. Expected either 'link' or 'meta'`);
				return null;
			}
			let Comp = tagName;
			return /* @__PURE__ */ import_react.createElement(Comp, {
				key: JSON.stringify(rest),
				...rest
			});
		}
		if ("title" in metaProps) return /* @__PURE__ */ import_react.createElement("title", { key: "title" }, String(metaProps.title));
		if ("charset" in metaProps) {
			metaProps.charSet ??= metaProps.charset;
			delete metaProps.charset;
		}
		if ("charSet" in metaProps && metaProps.charSet != null) return typeof metaProps.charSet === "string" ? /* @__PURE__ */ import_react.createElement("meta", {
			key: "charSet",
			charSet: metaProps.charSet
		}) : null;
		if ("script:ld+json" in metaProps) try {
			let json = JSON.stringify(metaProps["script:ld+json"]);
			return /* @__PURE__ */ import_react.createElement("script", {
				key: `script:ld+json:${json}`,
				type: "application/ld+json",
				dangerouslySetInnerHTML: { __html: escapeHtml(json) }
			});
		} catch (e) {
			return null;
		}
		return /* @__PURE__ */ import_react.createElement("meta", {
			key: JSON.stringify(metaProps),
			...metaProps
		});
	}));
}
function isValidMetaTag(tagName) {
	return typeof tagName === "string" && /^(meta|link)$/.test(tagName);
}
/**
* Tracks whether hydration is finished, so scripts can be skipped
* during client-side updates.
*/
var isHydrated = false;
function setIsHydrated() {
	isHydrated = true;
}
/**
* Renders the client runtime of your app. It should be rendered inside the
* [`<body>`](https://developer.mozilla.org/en-US/docs/Web/HTML/Element/body)
*  of the document.
*
* If server rendering, you can omit `<Scripts/>` and the app will work as a
* traditional web app without JavaScript, relying solely on HTML and browser
* behaviors.
*
* @example
* import { Scripts } from "react-router";
*
* export default function Root() {
*   return (
*     <html>
*       <head />
*       <body>
*         <Scripts />
*       </body>
*     </html>
*   );
* }
*
* @public
* @category Components
* @mode framework
* @param scriptProps Additional props to spread onto the [`<script>`](https://developer.mozilla.org/en-US/docs/Web/HTML/Element/script)
* tags, such as [`crossOrigin`](https://developer.mozilla.org/en-US/docs/Web/API/HTMLScriptElement/crossOrigin),
* [`nonce`](https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Global_attributes/nonce),
* etc.
* @returns A collection of React elements for [`<script>`](https://developer.mozilla.org/en-US/docs/Web/HTML/Element/script)
* tags
*/
function Scripts(scriptProps) {
	let { manifest, serverHandoffString, isSpaMode, renderMeta, routeDiscovery, ssr, nonce: contextNonce } = useFrameworkContext();
	let { router, static: isStatic, staticContext } = useDataRouterContext$1();
	let { matches: routerMatches } = useDataRouterStateContext();
	let isRSCRouterContext = useIsRSCRouterContext();
	let enableFogOfWar = isFogOfWarEnabled(routeDiscovery, ssr);
	if (scriptProps.nonce == null && contextNonce) scriptProps = {
		...scriptProps,
		nonce: contextNonce
	};
	if (renderMeta) renderMeta.didRenderScripts = true;
	let matches = getActiveMatches(routerMatches, null, isSpaMode);
	import_react.useEffect(() => {
		setIsHydrated();
	}, []);
	let initialScripts = import_react.useMemo(() => {
		if (isRSCRouterContext) return null;
		let contextScript = staticContext ? `window.__reactRouterContext = ${serverHandoffString};window.__reactRouterContext.stream = new ReadableStream({start(controller){window.__reactRouterContext.streamController = controller;}}).pipeThrough(new TextEncoderStream());` : " ";
		let routeModulesScript = !isStatic ? " " : `${manifest.hmr?.runtime ? `import ${JSON.stringify(manifest.hmr.runtime)};` : ""}${!enableFogOfWar ? `import ${JSON.stringify(manifest.url)}` : ""};
${matches.map((match, routeIndex) => {
			let routeVarName = `route${routeIndex}`;
			let manifestEntry = manifest.routes[match.route.id];
			invariant(manifestEntry, `Route ${match.route.id} not found in manifest`);
			let { clientActionModule, clientLoaderModule, clientMiddlewareModule, hydrateFallbackModule, module } = manifestEntry;
			let chunks = [
				...clientActionModule ? [{
					module: clientActionModule,
					varName: `${routeVarName}_clientAction`
				}] : [],
				...clientLoaderModule ? [{
					module: clientLoaderModule,
					varName: `${routeVarName}_clientLoader`
				}] : [],
				...clientMiddlewareModule ? [{
					module: clientMiddlewareModule,
					varName: `${routeVarName}_clientMiddleware`
				}] : [],
				...hydrateFallbackModule ? [{
					module: hydrateFallbackModule,
					varName: `${routeVarName}_HydrateFallback`
				}] : [],
				{
					module,
					varName: `${routeVarName}_main`
				}
			];
			if (chunks.length === 1) return `import * as ${routeVarName} from ${JSON.stringify(module)};`;
			return [chunks.map((chunk) => `import * as ${chunk.varName} from "${chunk.module}";`).join("\n"), `const ${routeVarName} = {${chunks.map((chunk) => `...${chunk.varName}`).join(",")}};`].join("\n");
		}).join("\n")}
  ${enableFogOfWar ? `window.__reactRouterManifest = ${JSON.stringify(getPartialManifest(manifest, router), null, 2)};` : ""}
  window.__reactRouterRouteModules = {${matches.map((match, index) => `${JSON.stringify(match.route.id)}:route${index}`).join(",")}};

import(${JSON.stringify(manifest.entry.module)});`;
		return /* @__PURE__ */ import_react.createElement(import_react.Fragment, null, /* @__PURE__ */ import_react.createElement("script", {
			...scriptProps,
			suppressHydrationWarning: true,
			dangerouslySetInnerHTML: { __html: contextScript },
			type: void 0
		}), /* @__PURE__ */ import_react.createElement("script", {
			...scriptProps,
			suppressHydrationWarning: true,
			dangerouslySetInnerHTML: { __html: routeModulesScript },
			type: "module",
			async: true
		}));
	}, []);
	let preloads = isHydrated || isRSCRouterContext ? [] : [...new Set(manifest.entry.imports.concat(getModuleLinkHrefs(matches, manifest, { includeHydrateFallback: true })))];
	let sri = typeof manifest.sri === "object" ? manifest.sri : {};
	warnOnce(!isRSCRouterContext, "The <Scripts /> element is a no-op when using RSC and can be safely removed.");
	return isHydrated || isRSCRouterContext ? null : /* @__PURE__ */ import_react.createElement(import_react.Fragment, null, typeof manifest.sri === "object" ? /* @__PURE__ */ import_react.createElement("script", {
		...scriptProps,
		"rr-importmap": "",
		type: "importmap",
		suppressHydrationWarning: true,
		dangerouslySetInnerHTML: { __html: JSON.stringify({ integrity: sri }) }
	}) : null, !enableFogOfWar ? /* @__PURE__ */ import_react.createElement("link", {
		rel: "modulepreload",
		href: manifest.url,
		crossOrigin: scriptProps.crossOrigin,
		integrity: sri[manifest.url],
		nonce: scriptProps.nonce,
		suppressHydrationWarning: true
	}) : null, /* @__PURE__ */ import_react.createElement("link", {
		rel: "modulepreload",
		href: manifest.entry.module,
		crossOrigin: scriptProps.crossOrigin,
		integrity: sri[manifest.entry.module],
		nonce: scriptProps.nonce,
		suppressHydrationWarning: true
	}), preloads.map((path) => /* @__PURE__ */ import_react.createElement("link", {
		key: path,
		rel: "modulepreload",
		href: path,
		crossOrigin: scriptProps.crossOrigin,
		integrity: sri[path],
		nonce: scriptProps.nonce,
		suppressHydrationWarning: true
	})), initialScripts);
}
function mergeRefs(...refs) {
	return (value) => {
		refs.forEach((ref) => {
			if (typeof ref === "function") ref(value);
			else if (ref != null) ref.current = value;
		});
	};
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/dom/ssr/errorBoundaries.js
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
var RemixErrorBoundary = class extends import_react.Component {
	constructor(props) {
		super(props);
		this.state = {
			error: props.error || null,
			location: props.location
		};
	}
	static getDerivedStateFromError(error) {
		return { error };
	}
	static getDerivedStateFromProps(props, state) {
		if (state.location !== props.location) return {
			error: props.error || null,
			location: props.location
		};
		return {
			error: props.error || state.error,
			location: state.location
		};
	}
	render() {
		if (this.state.error) return /* @__PURE__ */ import_react.createElement(RemixRootDefaultErrorBoundary, {
			error: this.state.error,
			isOutsideRemixApp: true
		});
		else return this.props.children;
	}
};
/**
* When app's don't provide a root level ErrorBoundary, we default to this.
*/
function RemixRootDefaultErrorBoundary({ error, isOutsideRemixApp }) {
	let { nonce } = useFrameworkContext();
	console.error(error);
	let heyDeveloper = /* @__PURE__ */ import_react.createElement("script", {
		nonce,
		dangerouslySetInnerHTML: { __html: `
        console.log(
          "💿 Hey developer 👋. You can provide a way better UX than this when your app throws errors. Check out https://reactrouter.com/how-to/error-boundary for more information."
        );
      ` }
	});
	if (isRouteErrorResponse(error)) return /* @__PURE__ */ import_react.createElement(BoundaryShell, { title: "Unhandled Thrown Response!" }, /* @__PURE__ */ import_react.createElement("h1", { style: { fontSize: "24px" } }, error.status, " ", error.statusText), null);
	let errorInstance;
	if (error instanceof Error) errorInstance = error;
	else {
		let errorString = error == null ? "Unknown Error" : typeof error === "object" && "toString" in error ? error.toString() : JSON.stringify(error);
		errorInstance = new Error(errorString);
	}
	return /* @__PURE__ */ import_react.createElement(BoundaryShell, {
		title: "Application Error!",
		isOutsideRemixApp
	}, /* @__PURE__ */ import_react.createElement("h1", { style: { fontSize: "24px" } }, "Application Error"), /* @__PURE__ */ import_react.createElement("pre", { style: {
		padding: "2rem",
		background: "hsla(10, 50%, 50%, 0.1)",
		color: "red",
		overflow: "auto"
	} }, errorInstance.stack), heyDeveloper);
}
function BoundaryShell({ title, renderScripts, isOutsideRemixApp, children }) {
	let { routeModules } = useFrameworkContext();
	if (routeModules.root?.Layout && !isOutsideRemixApp) return children;
	return /* @__PURE__ */ import_react.createElement("html", { lang: "en" }, /* @__PURE__ */ import_react.createElement("head", null, /* @__PURE__ */ import_react.createElement("meta", { charSet: "utf-8" }), /* @__PURE__ */ import_react.createElement("meta", {
		name: "viewport",
		content: "width=device-width,initial-scale=1,viewport-fit=cover"
	}), /* @__PURE__ */ import_react.createElement("title", null, title)), /* @__PURE__ */ import_react.createElement("body", null, /* @__PURE__ */ import_react.createElement("main", { style: {
		fontFamily: "system-ui, sans-serif",
		padding: "2rem"
	} }, children, renderScripts ? /* @__PURE__ */ import_react.createElement(Scripts, null) : null)));
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/dom/lib.js
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
var isBrowser = typeof window !== "undefined" && typeof window.document !== "undefined" && typeof window.document.createElement !== "undefined";
try {
	if (isBrowser) window.__reactRouterVersion = "8.3.0";
} catch (e) {}
/**
* A declarative {@link Router | `<Router>`} that accepts a pre-instantiated
* `history` object.
* It's important to note that using your own `history` object is highly discouraged
* and may add two versions of the `history` library to your bundles unless you use
* the same version of the `history` library that React Router uses internally.
*
* @name unstable_HistoryRouter
* @public
* @category Declarative Routers
* @mode declarative
* @param props Props
* @param {HistoryRouterProps.basename} props.basename n/a
* @param {HistoryRouterProps.children} props.children n/a
* @param {HistoryRouterProps.history} props.history n/a
* @param {HistoryRouterProps.useTransitions} props.useTransitions n/a
* @returns A declarative {@link Router | `<Router>`} using the provided history
* implementation for client-side routing.
*/
function HistoryRouter({ basename, children, history, useTransitions }) {
	let [state, setStateImpl] = import_react.useState({
		action: history.action,
		location: history.location
	});
	let setState = import_react.useCallback((newState) => {
		if (useTransitions === false) setStateImpl(newState);
		else import_react.startTransition(() => setStateImpl(newState));
	}, [useTransitions]);
	import_react.useLayoutEffect(() => history.listen(setState), [history, setState]);
	return /* @__PURE__ */ import_react.createElement(Router, {
		basename,
		children,
		location: state.location,
		navigationType: state.action,
		navigator: history,
		useTransitions
	});
}
HistoryRouter.displayName = "unstable_HistoryRouter";
/**
* A progressively enhanced [`<a href>`](https://developer.mozilla.org/en-US/docs/Web/HTML/Element/a)
* wrapper to enable navigation with client-side routing.
*
* @example
* import { Link } from "react-router";
*
* <Link to="/dashboard">Dashboard</Link>;
*
* <Link
*   to={{
*     pathname: "/some/path",
*     search: "?query=string",
*     hash: "#hash",
*   }}
* />;
*
* @public
* @category Components
* @param {LinkProps.discover} props.discover [modes: framework] n/a
* @param {LinkProps.prefetch} props.prefetch [modes: framework] n/a
* @param {LinkProps.preventScrollReset} props.preventScrollReset [modes: framework, data] n/a
* @param {LinkProps.relative} props.relative n/a
* @param {LinkProps.reloadDocument} props.reloadDocument n/a
* @param {LinkProps.replace} props.replace n/a
* @param {LinkProps.state} props.state n/a
* @param {LinkProps.to} props.to n/a
* @param {LinkProps.viewTransition} props.viewTransition [modes: framework, data] n/a
* @param {LinkProps.defaultShouldRevalidate} props.defaultShouldRevalidate n/a
* @param {LinkProps.mask} props.mask [modes: framework, data] n/a
*/
var Link = import_react.forwardRef(function LinkWithRef({ onClick, discover = "render", prefetch = "none", relative, reloadDocument, replace, mask, state, target, to, preventScrollReset, viewTransition, defaultShouldRevalidate, ...rest }, forwardedRef) {
	let { basename, navigator, useTransitions } = import_react.useContext(NavigationContext);
	let isAbsolute = typeof to === "string" && ABSOLUTE_URL_REGEX.test(to);
	let parsed = parseToInfo(to, basename);
	to = parsed.to;
	let href = useHref(to, { relative });
	let location = useLocation();
	let maskedHref = null;
	if (mask) {
		let resolved = resolveTo(mask, [], location.mask ? location.mask.pathname : "/", true);
		if (basename !== "/") resolved.pathname = resolved.pathname === "/" ? basename : joinPaths([basename, resolved.pathname]);
		maskedHref = navigator.createHref(resolved);
	}
	let [shouldPrefetch, prefetchRef, prefetchHandlers] = usePrefetchBehavior(prefetch, rest);
	let internalOnClick = useLinkClickHandler(to, {
		replace,
		mask,
		state,
		target,
		preventScrollReset,
		relative,
		viewTransition,
		defaultShouldRevalidate,
		useTransitions
	});
	function handleClick(event) {
		if (onClick) onClick(event);
		if (!event.defaultPrevented) internalOnClick(event);
	}
	let isSpaLink = !(parsed.isExternal || reloadDocument);
	let link = /* @__PURE__ */ import_react.createElement("a", {
		...rest,
		...prefetchHandlers,
		href: (isSpaLink ? maskedHref : void 0) || parsed.absoluteURL || href,
		onClick: isSpaLink ? handleClick : onClick,
		ref: mergeRefs(forwardedRef, prefetchRef),
		target,
		"data-discover": !isAbsolute && discover === "render" ? "true" : void 0
	});
	return shouldPrefetch && !isAbsolute ? /* @__PURE__ */ import_react.createElement(import_react.Fragment, null, link, /* @__PURE__ */ import_react.createElement(PrefetchPageLinks, { page: href })) : link;
});
Link.displayName = "Link";
/**
* Wraps {@link Link | `<Link>`} with additional props for styling active and
* pending states.
*
* - Automatically applies classes to the link based on its `active` and `pending`
* states, see {@link NavLinkProps.className}
*   - Note that `pending` is only available with Framework and Data modes.
* - Automatically applies `aria-current="page"` to the link when the link is active.
* See [`aria-current`](https://developer.mozilla.org/en-US/docs/Web/Accessibility/ARIA/Attributes/aria-current)
* on MDN.
* - States are additionally available through the className, style, and children
* render props. See {@link NavLinkRenderProps}.
*
* @example
* <NavLink to="/message">Messages</NavLink>
*
* // Using render props
* <NavLink
*   to="/messages"
*   className={({ isActive, isPending }) =>
*     isPending ? "pending" : isActive ? "active" : ""
*   }
* >
*   Messages
* </NavLink>
*
* @public
* @category Components
* @param {NavLinkProps.caseSensitive} props.caseSensitive n/a
* @param {NavLinkProps.children} props.children n/a
* @param {NavLinkProps.className} props.className n/a
* @param {NavLinkProps.discover} props.discover [modes: framework] n/a
* @param {NavLinkProps.end} props.end n/a
* @param {NavLinkProps.prefetch} props.prefetch [modes: framework] n/a
* @param {NavLinkProps.preventScrollReset} props.preventScrollReset [modes: framework, data] n/a
* @param {NavLinkProps.relative} props.relative n/a
* @param {NavLinkProps.reloadDocument} props.reloadDocument n/a
* @param {NavLinkProps.replace} props.replace n/a
* @param {NavLinkProps.state} props.state n/a
* @param {NavLinkProps.style} props.style n/a
* @param {NavLinkProps.to} props.to n/a
* @param {NavLinkProps.viewTransition} props.viewTransition [modes: framework, data] n/a
*/
var NavLink = import_react.forwardRef(function NavLinkWithRef({ "aria-current": ariaCurrentProp = "page", caseSensitive = false, className: classNameProp = "", end = false, style: styleProp, to, viewTransition, children, ...rest }, ref) {
	let path = useResolvedPath(to, { relative: rest.relative });
	let location = useLocation();
	let routerState = import_react.useContext(DataRouterStateContext);
	let { navigator, basename } = import_react.useContext(NavigationContext);
	let isTransitioning = routerState != null && useViewTransitionState(path) && viewTransition === true;
	let toPathname = navigator.encodeLocation ? navigator.encodeLocation(path).pathname : path.pathname;
	let locationPathname = location.pathname;
	let nextLocationPathname = routerState && routerState.navigation && routerState.navigation.location ? routerState.navigation.location.pathname : null;
	if (!caseSensitive) {
		locationPathname = locationPathname.toLowerCase();
		nextLocationPathname = nextLocationPathname ? nextLocationPathname.toLowerCase() : null;
		toPathname = toPathname.toLowerCase();
	}
	if (nextLocationPathname && basename) nextLocationPathname = stripBasename(nextLocationPathname, basename) || nextLocationPathname;
	const endSlashPosition = toPathname !== "/" && toPathname.endsWith("/") ? toPathname.length - 1 : toPathname.length;
	let isActive = locationPathname === toPathname || !end && locationPathname.startsWith(toPathname) && locationPathname.charAt(endSlashPosition) === "/";
	let isPending = nextLocationPathname != null && (nextLocationPathname === toPathname || !end && nextLocationPathname.startsWith(toPathname) && nextLocationPathname.charAt(endSlashPosition) === "/");
	let renderProps = {
		isActive,
		isPending,
		isTransitioning
	};
	let ariaCurrent = isActive ? ariaCurrentProp : void 0;
	let className;
	if (typeof classNameProp === "function") className = classNameProp(renderProps);
	else className = [
		classNameProp,
		isActive ? "active" : null,
		isPending ? "pending" : null,
		isTransitioning ? "transitioning" : null
	].filter(Boolean).join(" ");
	let style = typeof styleProp === "function" ? styleProp(renderProps) : styleProp;
	return /* @__PURE__ */ import_react.createElement(Link, {
		...rest,
		"aria-current": ariaCurrent,
		className,
		ref,
		style,
		to,
		viewTransition
	}, typeof children === "function" ? children(renderProps) : children);
});
NavLink.displayName = "NavLink";
/**
* A progressively enhanced HTML [`<form>`](https://developer.mozilla.org/en-US/docs/Web/HTML/Element/form)
* that submits data to actions via [`fetch`](https://developer.mozilla.org/en-US/docs/Web/API/fetch),
* activating pending states in {@link useNavigation} which enables advanced
* user interfaces beyond a basic HTML [`<form>`](https://developer.mozilla.org/en-US/docs/Web/HTML/Element/form).
* After a form's `action` completes, all data on the page is automatically
* revalidated to keep the UI in sync with the data.
*
* Because it uses the HTML form API, server rendered pages are interactive at a
* basic level before JavaScript loads. Instead of React Router managing the
* submission, the browser manages the submission as well as the pending states
* (like the spinning favicon). After JavaScript loads, React Router takes over
* enabling web application user experiences.
*
* `Form` is most useful for submissions that should also change the URL or
* otherwise add an entry to the browser history stack. For forms that shouldn't
* manipulate the browser [`History`](https://developer.mozilla.org/en-US/docs/Web/API/History)
* stack, use {@link FetcherWithComponents.Form | `<fetcher.Form>`}.
*
* @example
* import { Form } from "react-router";
*
* function NewEvent() {
*   return (
*     <Form action="/events" method="post">
*       <input name="title" type="text" />
*       <input name="description" type="text" />
*     </Form>
*   );
* }
*
* @public
* @category Components
* @mode framework
* @mode data
* @param {FormProps.action} action n/a
* @param {FormProps.discover} discover n/a
* @param {FormProps.encType} encType n/a
* @param {FormProps.fetcherKey} fetcherKey n/a
* @param {FormProps.method} method n/a
* @param {FormProps.navigate} navigate n/a
* @param {FormProps.preventScrollReset} preventScrollReset n/a
* @param {FormProps.relative} relative n/a
* @param {FormProps.reloadDocument} reloadDocument n/a
* @param {FormProps.replace} replace n/a
* @param {FormProps.state} state n/a
* @param {FormProps.viewTransition} viewTransition n/a
* @param {FormProps.defaultShouldRevalidate} defaultShouldRevalidate n/a
* @returns A progressively enhanced [`<form>`](https://developer.mozilla.org/en-US/docs/Web/HTML/Element/form) component
*/
var Form = import_react.forwardRef(({ discover = "render", fetcherKey, navigate, reloadDocument, replace, state, method = "get", action, onSubmit, relative, preventScrollReset, viewTransition, defaultShouldRevalidate, ...props }, forwardedRef) => {
	let { useTransitions } = import_react.useContext(NavigationContext);
	let submit = useSubmit();
	let formAction = useFormAction(action, { relative });
	let formMethod = method.toLowerCase() === "get" ? "get" : "post";
	let isAbsolute = typeof action === "string" && ABSOLUTE_URL_REGEX.test(action);
	let submitHandler = (event) => {
		onSubmit && onSubmit(event);
		if (event.defaultPrevented) return;
		event.preventDefault();
		let submitter = event.nativeEvent.submitter;
		let submitMethod = submitter?.getAttribute("formmethod") || method;
		let doSubmit = () => submit(submitter || event.currentTarget, {
			fetcherKey,
			method: submitMethod,
			navigate,
			replace,
			state,
			relative,
			preventScrollReset,
			viewTransition,
			defaultShouldRevalidate
		});
		if (useTransitions && navigate !== false) import_react.startTransition(() => doSubmit());
		else doSubmit();
	};
	return /* @__PURE__ */ import_react.createElement("form", {
		ref: forwardedRef,
		method: formMethod,
		action: formAction,
		onSubmit: reloadDocument ? onSubmit : submitHandler,
		...props,
		"data-discover": !isAbsolute && discover === "render" ? "true" : void 0
	});
});
Form.displayName = "Form";
/**
* Emulates the browser's scroll restoration on location changes. Apps should only render one of these, right before the {@link Scripts} component.
*
* ```tsx
* import { ScrollRestoration } from "react-router";
*
* export default function Root() {
*   return (
*     <html>
*       <body>
*         <ScrollRestoration />
*         <Scripts />
*       </body>
*     </html>
*   );
* }
* ```
*
* This component renders an inline `<script>` to prevent scroll flashing. The
* `nonce` prop will be passed down to the script tag to allow CSP nonce usage.
* If not provided in Framework Mode, it will default to any
* {@link ServerRouter | `<ServerRouter nonce>`} prop.
*
* ```tsx
* <ScrollRestoration nonce={cspNonce} />
* ```
*
* @public
* @category Components
* @mode framework
* @mode data
* @param props Props
* @param {ScrollRestorationProps.getKey} props.getKey n/a
* @param {ScriptsProps.nonce} props.nonce n/a
* @param {ScrollRestorationProps.storageKey} props.storageKey n/a
* @returns A [`<script>`](https://developer.mozilla.org/en-US/docs/Web/HTML/Element/script)
* tag that restores scroll positions on navigation.
*/
function ScrollRestoration({ getKey, storageKey, ...props }) {
	let remixContext = import_react.useContext(FrameworkContext);
	let { basename } = import_react.useContext(NavigationContext);
	let location = useLocation();
	let matches = useMatches();
	useScrollRestoration({
		getKey,
		storageKey
	});
	let ssrKey = import_react.useMemo(() => {
		if (!remixContext || !getKey) return null;
		let userKey = getScrollRestorationKey(location, matches, basename, getKey);
		return userKey !== location.key ? userKey : null;
	}, []);
	if (!remixContext || remixContext.isSpaMode) return null;
	let restoreScroll = ((storageKey, restoreKey) => {
		if (!window.history.state || !window.history.state.key) {
			let key = Math.random().toString(32).slice(2);
			window.history.replaceState({ key }, "");
		}
		try {
			let storedY = JSON.parse(sessionStorage.getItem(storageKey) || "{}")[restoreKey || window.history.state.key];
			if (typeof storedY === "number") window.scrollTo(0, storedY);
		} catch (error) {
			console.error(error);
			sessionStorage.removeItem(storageKey);
		}
	}).toString();
	if (props.nonce == null && remixContext?.nonce) props.nonce = remixContext.nonce;
	return /* @__PURE__ */ import_react.createElement("script", {
		...props,
		suppressHydrationWarning: true,
		dangerouslySetInnerHTML: { __html: `(${restoreScroll})(${escapeHtml(JSON.stringify(storageKey || SCROLL_RESTORATION_STORAGE_KEY))}, ${escapeHtml(JSON.stringify(ssrKey))})` }
	});
}
ScrollRestoration.displayName = "ScrollRestoration";
function getDataRouterConsoleError(hookName) {
	return `${hookName} must be used within a data router.  See https://reactrouter.com/en/main/routers/picking-a-router.`;
}
function useDataRouterContext(hookName) {
	let ctx = import_react.useContext(DataRouterContext);
	invariant$1(ctx, getDataRouterConsoleError(hookName));
	return ctx;
}
function useDataRouterState(hookName) {
	let state = import_react.useContext(DataRouterStateContext);
	invariant$1(state, getDataRouterConsoleError(hookName));
	return state;
}
/**
* Handles the click behavior for router {@link Link | `<Link>`} components.This
* is useful if you need to create custom {@link Link | `<Link>`} components with
* the same click behavior we use in our exported {@link Link | `<Link>`}.
*
* @public
* @category Hooks
* @param to The URL to navigate to, can be a string or a partial {@link Path}.
* @param options Options
* @param options.preventScrollReset Whether to prevent the scroll position from
* being reset to the top of the viewport on completion of the navigation when
* using the {@link ScrollRestoration} component. Defaults to `false`.
* @param options.relative The {@link RelativeRoutingType | relative routing type}
* to use for the link. Defaults to `"route"`.
* @param options.replace Whether to replace the current [`History`](https://developer.mozilla.org/en-US/docs/Web/API/History)
* entry instead of pushing a new one. Defaults to `false`.
* @param options.state The state to add to the [`History`](https://developer.mozilla.org/en-US/docs/Web/API/History)
* entry for this navigation. Defaults to `undefined`.
* @param options.target The target attribute for the link. Defaults to `undefined`.
* @param options.viewTransition Enables a [View Transition](https://developer.mozilla.org/en-US/docs/Web/API/View_Transitions_API)
* for this navigation. To apply specific styles during the transition, see
* {@link useViewTransitionState}. Defaults to `false`.
* @param options.defaultShouldRevalidate Specify the default revalidation
* behavior for the navigation. When not specified, loaders revalidate
* according to the router's standard revalidation behavior.
* @param options.mask Masked location to display in the browser instead
* of the router location. Defaults to `undefined`.
* @param options.useTransitions Wraps the navigation in
* [`React.startTransition`](https://react.dev/reference/react/startTransition)
* for concurrent rendering. Defaults to `false`.
* @returns A click handler function that can be used in a custom {@link Link} component.
*/
function useLinkClickHandler(to, { target, replace: replaceProp, mask, state, preventScrollReset, relative, viewTransition, defaultShouldRevalidate, useTransitions } = {}) {
	let navigate = useNavigate();
	let location = useLocation();
	let path = useResolvedPath(to, { relative });
	return import_react.useCallback((event) => {
		if (shouldProcessLinkClick(event, target)) {
			event.preventDefault();
			let replace = replaceProp !== void 0 ? replaceProp : createPath(location) === createPath(path);
			let doNavigate = () => navigate(to, {
				replace,
				mask,
				state,
				preventScrollReset,
				relative,
				viewTransition,
				defaultShouldRevalidate
			});
			if (useTransitions) import_react.startTransition(() => doNavigate());
			else doNavigate();
		}
	}, [
		location,
		navigate,
		path,
		replaceProp,
		mask,
		state,
		target,
		to,
		preventScrollReset,
		relative,
		viewTransition,
		defaultShouldRevalidate,
		useTransitions
	]);
}
var fetcherId = 0;
var getUniqueFetcherId = () => `__${String(++fetcherId)}__`;
/**
* The imperative version of {@link Form | `<Form>`} that lets you submit a form
* from code instead of a user interaction.
*
* @example
* import { useSubmit } from "react-router";
*
* function SomeComponent() {
*   const submit = useSubmit();
*   return (
*     <Form onChange={(event) => submit(event.currentTarget)} />
*   );
* }
*
* @public
* @category Hooks
* @mode framework
* @mode data
* @returns A function that can be called to submit a {@link Form} imperatively.
*/
function useSubmit() {
	let { router } = useDataRouterContext("useSubmit");
	let { basename } = import_react.useContext(NavigationContext);
	let currentRouteId = useRouteId();
	let routerFetch = router.fetch;
	let routerNavigate = router.navigate;
	return import_react.useCallback(async (target, options = {}) => {
		let { action, method, encType, formData, body } = getFormSubmissionInfo(target, basename);
		if (options.navigate === false) await routerFetch(options.fetcherKey || getUniqueFetcherId(), currentRouteId, options.action || action, {
			defaultShouldRevalidate: options.defaultShouldRevalidate,
			preventScrollReset: options.preventScrollReset,
			formData,
			body,
			formMethod: options.method || method,
			formEncType: options.encType || encType,
			flushSync: options.flushSync
		});
		else await routerNavigate(options.action || action, {
			defaultShouldRevalidate: options.defaultShouldRevalidate,
			preventScrollReset: options.preventScrollReset,
			formData,
			body,
			formMethod: options.method || method,
			formEncType: options.encType || encType,
			replace: options.replace,
			state: options.state,
			fromRouteId: currentRouteId,
			flushSync: options.flushSync,
			viewTransition: options.viewTransition
		});
	}, [
		routerFetch,
		routerNavigate,
		basename,
		currentRouteId
	]);
}
/**
* Resolves the URL to the closest route in the component hierarchy instead of
* the current URL of the app.
*
* This is used internally by {@link Form} to resolve the `action` to the closest
* route, but can be used generically as well.
*
* ```ts
* import { useFormAction } from "react-router";
*
* function SomeComponent() {
*   // closest route URL
*   let action = useFormAction();
*
*   // closest route URL + "destroy"
*   let destroyAction = useFormAction("destroy");
* }
* ```
*
* <docs-info>This hook adds a `basename` if your app specifies one, so that it
* can be used with raw `<form>` elements in a progressively enhanced way.  If
* you are using this to provide an `action` to `<Form>` or `fetcher.submit`, you
* will need to remove the `basename` since both of those will prepend it
* internally.</docs-info>
*
*
* @public
* @category Hooks
* @mode framework
* @mode data
* @param action The action to append to the closest route URL. Defaults to the
* closest route URL.
* @param options Options
* @param options.relative The relative routing type to use when resolving the
* action. Defaults to `"route"`.
* @returns The resolved action URL.
*/
function useFormAction(action, { relative } = {}) {
	let { basename } = import_react.useContext(NavigationContext);
	let routeContext = import_react.useContext(RouteContext);
	invariant$1(routeContext, "useFormAction must be used inside a RouteContext");
	let [match] = routeContext.matches.slice(-1);
	let path = { ...useResolvedPath(action ? action : ".", { relative }) };
	let location = useLocation();
	if (action == null) {
		path.search = location.search;
		let params = new URLSearchParams(path.search);
		let indexValues = params.getAll("index");
		if (indexValues.some((v) => v === "")) {
			params.delete("index");
			indexValues.filter((v) => v).forEach((v) => params.append("index", v));
			let qs = params.toString();
			path.search = qs ? `?${qs}` : "";
		}
	}
	if ((!action || action === ".") && match.route.index) path.search = path.search ? path.search.replace(/^\?/, "?index&") : "?index";
	if (basename !== "/") path.pathname = path.pathname === "/" ? basename : joinPaths([basename, path.pathname]);
	return createPath(path);
}
var SCROLL_RESTORATION_STORAGE_KEY = "react-router-scroll-positions";
var savedScrollPositions = {};
function getScrollRestorationKey(location, matches, basename, getKey) {
	let key = null;
	if (getKey) if (basename !== "/") key = getKey({
		...location,
		pathname: stripBasename(location.pathname, basename) || location.pathname
	}, matches);
	else key = getKey(location, matches);
	if (key == null) key = location.key;
	return key;
}
/**
* When rendered inside a {@link RouterProvider}, will restore scroll positions
* on navigations
*
* <!--
* Not marked `@public` because we only export as UNSAFE_ and therefore we don't
* maintain an .md file for this hook
* -->
*
* @name UNSAFE_useScrollRestoration
* @category Hooks
* @mode framework
* @mode data
* @param options Options
* @param options.getKey A function that returns a key to use for scroll restoration.
* This is useful for custom scroll restoration logic, such as using only the pathname
* so that subsequent navigations to prior paths will restore the scroll. Defaults
* to `location.key`.
* @param options.storageKey The key to use for storing scroll positions in
* `sessionStorage`. Defaults to `"react-router-scroll-positions"`.
* @returns {void}
*/
function useScrollRestoration({ getKey, storageKey } = {}) {
	let { router } = useDataRouterContext("useScrollRestoration");
	let { restoreScrollPosition, preventScrollReset } = useDataRouterState("useScrollRestoration");
	let { basename } = import_react.useContext(NavigationContext);
	let location = useLocation();
	let matches = useMatches();
	let navigation = useNavigation();
	import_react.useEffect(() => {
		window.history.scrollRestoration = "manual";
		return () => {
			window.history.scrollRestoration = "auto";
		};
	}, []);
	usePageHide(import_react.useCallback(() => {
		if (navigation.state === "idle") {
			let key = getScrollRestorationKey(location, matches, basename, getKey);
			savedScrollPositions[key] = window.scrollY;
		}
		try {
			sessionStorage.setItem(storageKey || SCROLL_RESTORATION_STORAGE_KEY, JSON.stringify(savedScrollPositions));
		} catch (error) {
			warning(false, `Failed to save scroll positions in sessionStorage, <ScrollRestoration /> will not work properly (${error}).`);
		}
		window.history.scrollRestoration = "auto";
	}, [
		navigation.state,
		getKey,
		basename,
		location,
		matches,
		storageKey
	]));
	if (typeof document !== "undefined") {
		import_react.useLayoutEffect(() => {
			try {
				let sessionPositions = sessionStorage.getItem(storageKey || SCROLL_RESTORATION_STORAGE_KEY);
				if (sessionPositions) savedScrollPositions = JSON.parse(sessionPositions);
			} catch (e) {}
		}, [storageKey]);
		import_react.useLayoutEffect(() => {
			let disableScrollRestoration = router?.enableScrollRestoration(savedScrollPositions, () => window.scrollY, getKey ? (location, matches) => getScrollRestorationKey(location, matches, basename, getKey) : void 0);
			return () => disableScrollRestoration && disableScrollRestoration();
		}, [
			router,
			basename,
			getKey
		]);
		import_react.useLayoutEffect(() => {
			if (restoreScrollPosition === false) return;
			if (typeof restoreScrollPosition === "number") {
				window.scrollTo(0, restoreScrollPosition);
				return;
			}
			try {
				if (location.hash) {
					let el = document.getElementById(decodeURIComponent(location.hash.slice(1)));
					if (el) {
						el.scrollIntoView();
						return;
					}
				}
			} catch {
				warning(false, `"${location.hash.slice(1)}" is not a decodable element ID. The view will not scroll to it.`);
			}
			if (preventScrollReset === true) return;
			window.scrollTo(0, 0);
		}, [
			location,
			restoreScrollPosition,
			preventScrollReset
		]);
	}
}
function usePageHide(callback, options) {
	let { capture } = options || {};
	import_react.useEffect(() => {
		let opts = capture != null ? { capture } : void 0;
		window.addEventListener("pagehide", callback, opts);
		return () => {
			window.removeEventListener("pagehide", callback, opts);
		};
	}, [callback, capture]);
}
/**
* This hook returns `true` when there is an active [View Transition](https://developer.mozilla.org/en-US/docs/Web/API/View_Transitions_API)
* and the specified location matches either side of the navigation (the URL you are
* navigating **to** or the URL you are navigating **from**). This can be used to apply finer-grained styles to
* elements to further customize the view transition. This requires that view
* transitions have been enabled for the given navigation via {@link LinkProps.viewTransition}
* (or the `Form`, `submit`, or `navigate` call)
*
* @public
* @category Hooks
* @mode framework
* @mode data
* @param to The {@link To} location to compare against the active transition's current
* and next URLs.
* @param options Options
* @param options.relative The relative routing type to use when resolving the
* `to` location, defaults to `"route"`. See {@link RelativeRoutingType} for
* more details.
* @returns `true` if there is an active [View Transition](https://developer.mozilla.org/en-US/docs/Web/API/View_Transitions_API)
* and the resolved path matches the transition's destination or source pathname, otherwise `false`.
*/
function useViewTransitionState(to, { relative } = {}) {
	let vtContext = import_react.useContext(ViewTransitionContext);
	invariant$1(vtContext != null, "`useViewTransitionState` must be used within `react-router/dom`'s `RouterProvider`.  Did you accidentally import `RouterProvider` from `react-router`?");
	let { basename } = useDataRouterContext("useViewTransitionState");
	let path = useResolvedPath(to, { relative });
	if (!vtContext.isTransitioning) return false;
	let currentPath = stripBasename(vtContext.currentLocation.pathname, basename) || vtContext.currentLocation.pathname;
	let nextPath = stripBasename(vtContext.nextLocation.pathname, basename) || vtContext.nextLocation.pathname;
	return matchPath(path.pathname, nextPath) != null || matchPath(path.pathname, currentPath) != null;
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/dom/server.js
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
* A {@link DataRouter} that may not navigate to any other {@link Location}.
* This is useful on the server where there is no stateful UI.
*
* @example
* export async function handleRequest(request: Request) {
*   let { query, dataRoutes } = createStaticHandler(routes);
*   let context = await query(request));
*
*   if (context instanceof Response) {
*     return context;
*   }
*
*   let router = createStaticRouter(dataRoutes, context);
*   return new Response(
*     ReactDOMServer.renderToString(<StaticRouterProvider ... />),
*     { headers: { "Content-Type": "text/html" } }
*   );
* }
*
* @public
* @category Data Routers
* @mode data
* @param props Props
* @param {StaticRouterProviderProps.context} props.context n/a
* @param {StaticRouterProviderProps.hydrate} props.hydrate n/a
* @param {StaticRouterProviderProps.nonce} props.nonce n/a
* @param {StaticRouterProviderProps.router} props.router n/a
* @returns A React element that renders the static router provider
*/
function StaticRouterProvider({ context, router, hydrate = true, nonce }) {
	invariant$1(router && context, "You must provide `router` and `context` to <StaticRouterProvider>");
	let dataRouterContext = {
		router,
		navigator: getStatelessNavigator(),
		static: true,
		staticContext: context,
		basename: context.basename || "/"
	};
	let fetchersContext = /* @__PURE__ */ new Map();
	let hydrateScript = "";
	if (hydrate !== false) {
		let data = {
			loaderData: context.loaderData,
			actionData: context.actionData,
			errors: serializeErrors(context.errors)
		};
		hydrateScript = `window.__staticRouterHydrationData = JSON.parse(${escapeHtml(JSON.stringify(JSON.stringify(data)))});`;
	}
	let { state } = dataRouterContext.router;
	return /* @__PURE__ */ import_react.createElement(import_react.Fragment, null, /* @__PURE__ */ import_react.createElement(DataRouterContext.Provider, { value: dataRouterContext }, /* @__PURE__ */ import_react.createElement(DataRouterStateContext.Provider, { value: state }, /* @__PURE__ */ import_react.createElement(FetchersContext.Provider, { value: fetchersContext }, /* @__PURE__ */ import_react.createElement(ViewTransitionContext.Provider, { value: { isTransitioning: false } }, /* @__PURE__ */ import_react.createElement(Router, {
		basename: dataRouterContext.basename,
		location: state.location,
		navigationType: state.historyAction,
		navigator: dataRouterContext.navigator,
		static: dataRouterContext.static,
		useTransitions: false
	}, /* @__PURE__ */ import_react.createElement(DataRoutes, {
		manifest: router.manifest,
		routes: router.routes,
		future: router.future,
		state,
		isStatic: true
	})))))), hydrateScript ? /* @__PURE__ */ import_react.createElement("script", {
		suppressHydrationWarning: true,
		nonce,
		dangerouslySetInnerHTML: { __html: hydrateScript }
	}) : null);
}
function serializeErrors(errors) {
	if (!errors) return null;
	let entries = Object.entries(errors);
	let serialized = {};
	for (let [key, val] of entries) if (isRouteErrorResponse(val)) serialized[key] = {
		...val,
		__type: "RouteErrorResponse"
	};
	else if (val instanceof Error) serialized[key] = {
		message: val.message,
		__type: "Error",
		...val.name !== "Error" ? { __subType: val.name } : {}
	};
	else serialized[key] = val;
	return serialized;
}
function getStatelessNavigator() {
	return {
		createHref,
		encodeLocation,
		push(to) {
			throw new Error(`You cannot use navigator.push() on the server because it is a stateless environment. This error was probably triggered when you did a \`navigate(${JSON.stringify(to)})\` somewhere in your app.`);
		},
		replace(to) {
			throw new Error(`You cannot use navigator.replace() on the server because it is a stateless environment. This error was probably triggered when you did a \`navigate(${JSON.stringify(to)}, { replace: true })\` somewhere in your app.`);
		},
		go(delta) {
			throw new Error(`You cannot use navigator.go() on the server because it is a stateless environment. This error was probably triggered when you did a \`navigate(${delta})\` somewhere in your app.`);
		},
		back() {
			throw new Error("You cannot use navigator.back() on the server because it is a stateless environment.");
		},
		forward() {
			throw new Error("You cannot use navigator.forward() on the server because it is a stateless environment.");
		}
	};
}
/**
* Create a static {@link DataRouter} for server-side rendering
*
* @example
* export async function handleRequest(request: Request) {
*   let { query, dataRoutes } = createStaticHandler(routes);
*   let context = await query(request);
*
*   if (context instanceof Response) {
*     return context;
*   }
*
*   let router = createStaticRouter(dataRoutes, context);
*   return new Response(
*     ReactDOMServer.renderToString(<StaticRouterProvider ... />),
*     { headers: { "Content-Type": "text/html" } }
*   );
* }
*
* @public
* @category Data Routers
* @mode data
* @param routes The route objects to create a static {@link DataRouter} for
* @param context The {@link StaticHandlerContext} returned from {@link StaticHandler}'s
* `query`
* @param opts Options
* @param opts.future Future flags for the static {@link DataRouter}
* @param opts.branches Optional pre-computed route branches
* @returns A static {@link DataRouter} that can be used to render the provided routes
*/
function createStaticRouter(routes, context, opts = {}) {
	let manifest = {};
	let dataRoutes = convertRoutesToDataRoutes(routes, void 0, void 0, manifest);
	let matches = context.matches.map((match) => {
		let route = manifest[match.route.id] || match.route;
		return {
			...match,
			route
		};
	});
	let msg = (method) => `You cannot use router.${method}() on the server because it is a stateless environment`;
	return {
		get basename() {
			return context.basename;
		},
		get future() {
			return { ...opts?.future };
		},
		get state() {
			return {
				historyAction: "POP",
				location: context.location,
				matches,
				loaderData: context.loaderData,
				actionData: context.actionData,
				errors: context.errors,
				initialized: true,
				renderFallback: false,
				navigation: IDLE_NAVIGATION,
				restoreScrollPosition: null,
				preventScrollReset: false,
				revalidation: "idle",
				fetchers: /* @__PURE__ */ new Map(),
				blockers: /* @__PURE__ */ new Map()
			};
		},
		get routes() {
			return dataRoutes;
		},
		get branches() {
			return opts.branches;
		},
		get manifest() {
			return manifest;
		},
		get window() {},
		initialize() {
			throw msg("initialize");
		},
		subscribe() {
			throw msg("subscribe");
		},
		enableScrollRestoration() {
			throw msg("enableScrollRestoration");
		},
		navigate() {
			throw msg("navigate");
		},
		fetch() {
			throw msg("fetch");
		},
		revalidate() {
			throw msg("revalidate");
		},
		createHref,
		encodeLocation,
		getFetcher() {
			return IDLE_FETCHER;
		},
		deleteFetcher() {
			throw msg("deleteFetcher");
		},
		resetFetcher() {
			throw msg("resetFetcher");
		},
		dispose() {
			throw msg("dispose");
		},
		getBlocker() {
			return IDLE_BLOCKER;
		},
		deleteBlocker() {
			throw msg("deleteBlocker");
		},
		patchRoutes() {
			throw msg("patchRoutes");
		},
		_internalFetchControllers: /* @__PURE__ */ new Map(),
		_internalSetRoutes() {
			throw msg("_internalSetRoutes");
		},
		_internalSetStateDoNotUseOrYouWillBreakYourApp() {
			throw msg("_internalSetStateDoNotUseOrYouWillBreakYourApp");
		}
	};
}
function createHref(to) {
	return typeof to === "string" ? to : createPath(to);
}
function encodeLocation(to) {
	let href = typeof to === "string" ? to : createPath(to);
	href = href.replace(/ $/, "%20");
	let encoded = ABSOLUTE_URL_REGEX.test(href) ? new URL(href) : new URL(href, "http://localhost");
	return {
		pathname: encoded.pathname,
		search: encoded.search,
		hash: encoded.hash
	};
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/dom/ssr/server.js
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
* The server entry point for a React Router app in Framework Mode. This
* component is used to generate the HTML in the response from the server. See
* [`entry.server.tsx`](../framework-conventions/entry.server.tsx).
*
* @public
* @category Framework Routers
* @mode framework
* @param props Props
* @param {ServerRouterProps.context} props.context n/a
* @param {ServerRouterProps.nonce} props.nonce n/a
* @param {ServerRouterProps.url} props.url n/a
* @returns A React element that represents the server-rendered application.
*/
function ServerRouter({ context, url, nonce }) {
	if (typeof url === "string") url = new URL(url);
	let { manifest, routeModules, criticalCss, serverHandoffString } = context;
	let routes = createServerRoutes(manifest.routes, routeModules, context.future, context.isSpaMode);
	context.staticHandlerContext.loaderData = { ...context.staticHandlerContext.loaderData };
	for (let match of context.staticHandlerContext.matches) {
		let routeId = match.route.id;
		let route = routeModules[routeId];
		let manifestRoute = context.manifest.routes[routeId];
		if (route && manifestRoute && shouldHydrateRouteLoader(routeId, route.clientLoader, manifestRoute.hasLoader, context.isSpaMode) && (route.HydrateFallback || !manifestRoute.hasLoader)) delete context.staticHandlerContext.loaderData[routeId];
	}
	let router = createStaticRouter(routes, context.staticHandlerContext, { branches: context.branches });
	return /* @__PURE__ */ import_react.createElement(import_react.Fragment, null, /* @__PURE__ */ import_react.createElement(FrameworkContext.Provider, { value: {
		manifest,
		routeModules,
		criticalCss,
		serverHandoffString,
		future: context.future,
		ssr: context.ssr,
		isSpaMode: context.isSpaMode,
		routeDiscovery: context.routeDiscovery,
		nonce,
		serializeError: context.serializeError,
		renderMeta: context.renderMeta
	} }, /* @__PURE__ */ import_react.createElement(RemixErrorBoundary, { location: router.state.location }, /* @__PURE__ */ import_react.createElement(StaticRouterProvider, {
		router,
		context: context.staticHandlerContext,
		hydrate: false
	}))), context.serverHandoffStream ? /* @__PURE__ */ import_react.createElement(import_react.Suspense, null, /* @__PURE__ */ import_react.createElement(StreamTransfer, {
		context,
		identifier: 0,
		reader: context.serverHandoffStream.getReader(),
		textDecoder: new TextDecoder(),
		nonce
	})) : null);
}
//#endregion
//#region ../../node_modules/.pnpm/isbot@5.2.0/node_modules/isbot/index.mjs
var fullPattern = " daum[ /]| deusu/|(?:^|[^g])news(?!sapphire)|(?<! channel/|google/)google(?!(?:wv|app|/google| pixel))|(?<! cu)bots?(?:\\b|_)|(?<!cam)scan|(?<!lib)http|24x7|;\\s\\w+;$|@[a-z][\\w-]+\\.|\\(\\)|\\.com\\b|\\b\\w+\\.ai|\\bbw/|\\bdlc\\b|\\bort/|\\bperl\\b|\\btime/|\\||^[\\w \\.\\-\\(?:\\):%]+(?:/v?\\d+(?:\\.\\d+)?(?:\\.\\d{1,10})*?)?(?:,|$)|^[\\w\\-]+/[\\w]+$|^[^ ]{50,}$|^\\d+\\b|^\\W|^\\w*search\\b|^\\w+/[\\w\\(\\)]*$|^\\w+/\\d\\.\\d\\s\\([\\w@]+\\)$|^active|^ad muncher|^amaya|^apache/|^avsdevicesdk/|^azure|^biglotron|^blackbox exporter|^bot|^clamav[ /]|^claude-code/|^client/|^cobweb/|^custom|^ddg[_-]android|^discourse|^dispatch/\\d|^downcast/|^duckduckgo|^email|^exodusmovement|^facebook|^getright/|^gozilla/|^hobbit|^hotzonu|^hwcdn/|^igetter/|^jeode/|^jetty/|^jigsaw|^microsoft bits|^movabletype|^mozilla/\\d\\.\\d\\s[\\w\\.-]+$|^mozilla/\\d\\.\\d\\s\\((?:compatible;)?(?:\\s?[\\w\\d-.]+\\/\\d+\\.\\d+)?\\)$|^navermailapp|^netsurf|^offline|^openai/|^owler|^php|^postman|^ps_daily/|^python|^rank|^read|^reed|^remove\\.bg/|^rest|^rss|^snapchat|^sora |^space bison|^stape/|^svn|^swcd |^taringa|^thumbor/|^track|^w3c|^webbandit/|^webcopier|^wget|^whatsapp|^wordpress|^xenu link sleuth|^yahoo|^yandex|^zdm/\\d|^zoom marketplace/|abuse|advisor|agent\\b|analyzer|archive|ask jeeves/teoma|attracta|audit|bluecoat drtr|browsex|burpcollaborator|capture|catch|check\\b|checker|chrome-lighthouse|chromeframe|classifier|cloudflare|collapsify\\b|convertify|cookiehubverify/|crawl|cursor/|cypress/|dareboost|datanyze|dejaclick|detect|discovery|dmbrowser|download|exaleadcloudview|feed|fetcher|firephp|foregenix|functionize|grab|hardenize\\b|headless|hotjar|httrack|hubspot marketing grader|ibisbrowser|infrawatch|insight|inspect|iplabel|java(?!;)|library|linkcheck|linktiger|mail\\.ru/|manager|manus-user/|marketgoo/|measure|monitor\\b|neustar wpm|node\\b|nutch|offbyone|openvas|optimize|pageburst|pagespeed|parser|phantomjs|pingdom|playwright|powermarks|preview|productfinder|prospectingstudio|proxy|ptst[ /]\\d|radar|readable/|retriever|rexx;|rigor|rss\\b|scrape|securityheaders|selenium|server|silktide|sindup/|sogou|sparkler/|speedcurve|spider|splash|statuscake|supercleaner|synapse|synthetic|testlocally|tools|torrent|transcoder|upday/|url|validator|virtuoso|wappalyzer|watchtowr|webglance|webkit2png|whatcms/|xtate/";
var naivePattern = /bot|crawl|http|lighthouse|scan|search|spider/i;
var pattern;
function getPattern() {
	if (pattern instanceof RegExp) return pattern;
	try {
		pattern = new RegExp(fullPattern, "i");
	} catch (error) {
		pattern = naivePattern;
	}
	return pattern;
}
var isNonEmptyString = (value) => typeof value === "string" && value !== "";
function isBot(userAgent) {
	return isNonEmptyString(userAgent) && getPattern().test(userAgent);
}
var isbot = isBot;
//#endregion
//#region ../../node_modules/.pnpm/react-dom@19.2.7_react@19.2.7/node_modules/react-dom/cjs/react-dom.production.js
/**
* @license React
* react-dom.production.js
*
* Copyright (c) Meta Platforms, Inc. and affiliates.
*
* This source code is licensed under the MIT license found in the
* LICENSE file in the root directory of this source tree.
*/
var require_react_dom_production = /* @__PURE__ */ __commonJSMin(((exports) => {
	var React = require_react();
	function formatProdErrorMessage(code) {
		var url = "https://react.dev/errors/" + code;
		if (1 < arguments.length) {
			url += "?args[]=" + encodeURIComponent(arguments[1]);
			for (var i = 2; i < arguments.length; i++) url += "&args[]=" + encodeURIComponent(arguments[i]);
		}
		return "Minified React error #" + code + "; visit " + url + " for the full message or use the non-minified dev environment for full errors and additional helpful warnings.";
	}
	function noop() {}
	var Internals = {
		d: {
			f: noop,
			r: function() {
				throw Error(formatProdErrorMessage(522));
			},
			D: noop,
			C: noop,
			L: noop,
			m: noop,
			X: noop,
			S: noop,
			M: noop
		},
		p: 0,
		findDOMNode: null
	};
	var REACT_PORTAL_TYPE = Symbol.for("react.portal");
	function createPortal$1(children, containerInfo, implementation) {
		var key = 3 < arguments.length && void 0 !== arguments[3] ? arguments[3] : null;
		return {
			$$typeof: REACT_PORTAL_TYPE,
			key: null == key ? null : "" + key,
			children,
			containerInfo,
			implementation
		};
	}
	var ReactSharedInternals = React.__CLIENT_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE;
	function getCrossOriginStringAs(as, input) {
		if ("font" === as) return "";
		if ("string" === typeof input) return "use-credentials" === input ? input : "";
	}
	exports.__DOM_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE = Internals;
	exports.createPortal = function(children, container) {
		var key = 2 < arguments.length && void 0 !== arguments[2] ? arguments[2] : null;
		if (!container || 1 !== container.nodeType && 9 !== container.nodeType && 11 !== container.nodeType) throw Error(formatProdErrorMessage(299));
		return createPortal$1(children, container, null, key);
	};
	exports.flushSync = function(fn) {
		var previousTransition = ReactSharedInternals.T, previousUpdatePriority = Internals.p;
		try {
			if (ReactSharedInternals.T = null, Internals.p = 2, fn) return fn();
		} finally {
			ReactSharedInternals.T = previousTransition, Internals.p = previousUpdatePriority, Internals.d.f();
		}
	};
	exports.preconnect = function(href, options) {
		"string" === typeof href && (options ? (options = options.crossOrigin, options = "string" === typeof options ? "use-credentials" === options ? options : "" : void 0) : options = null, Internals.d.C(href, options));
	};
	exports.prefetchDNS = function(href) {
		"string" === typeof href && Internals.d.D(href);
	};
	exports.preinit = function(href, options) {
		if ("string" === typeof href && options && "string" === typeof options.as) {
			var as = options.as, crossOrigin = getCrossOriginStringAs(as, options.crossOrigin), integrity = "string" === typeof options.integrity ? options.integrity : void 0, fetchPriority = "string" === typeof options.fetchPriority ? options.fetchPriority : void 0;
			"style" === as ? Internals.d.S(href, "string" === typeof options.precedence ? options.precedence : void 0, {
				crossOrigin,
				integrity,
				fetchPriority
			}) : "script" === as && Internals.d.X(href, {
				crossOrigin,
				integrity,
				fetchPriority,
				nonce: "string" === typeof options.nonce ? options.nonce : void 0
			});
		}
	};
	exports.preinitModule = function(href, options) {
		if ("string" === typeof href) if ("object" === typeof options && null !== options) {
			if (null == options.as || "script" === options.as) {
				var crossOrigin = getCrossOriginStringAs(options.as, options.crossOrigin);
				Internals.d.M(href, {
					crossOrigin,
					integrity: "string" === typeof options.integrity ? options.integrity : void 0,
					nonce: "string" === typeof options.nonce ? options.nonce : void 0
				});
			}
		} else options ?? Internals.d.M(href);
	};
	exports.preload = function(href, options) {
		if ("string" === typeof href && "object" === typeof options && null !== options && "string" === typeof options.as) {
			var as = options.as, crossOrigin = getCrossOriginStringAs(as, options.crossOrigin);
			Internals.d.L(href, as, {
				crossOrigin,
				integrity: "string" === typeof options.integrity ? options.integrity : void 0,
				nonce: "string" === typeof options.nonce ? options.nonce : void 0,
				type: "string" === typeof options.type ? options.type : void 0,
				fetchPriority: "string" === typeof options.fetchPriority ? options.fetchPriority : void 0,
				referrerPolicy: "string" === typeof options.referrerPolicy ? options.referrerPolicy : void 0,
				imageSrcSet: "string" === typeof options.imageSrcSet ? options.imageSrcSet : void 0,
				imageSizes: "string" === typeof options.imageSizes ? options.imageSizes : void 0,
				media: "string" === typeof options.media ? options.media : void 0
			});
		}
	};
	exports.preloadModule = function(href, options) {
		if ("string" === typeof href) if (options) {
			var crossOrigin = getCrossOriginStringAs(options.as, options.crossOrigin);
			Internals.d.m(href, {
				as: "string" === typeof options.as && "script" !== options.as ? options.as : void 0,
				crossOrigin,
				integrity: "string" === typeof options.integrity ? options.integrity : void 0
			});
		} else Internals.d.m(href);
	};
	exports.requestFormReset = function(form) {
		Internals.d.r(form);
	};
	exports.unstable_batchedUpdates = function(fn, a) {
		return fn(a);
	};
	exports.useFormState = function(action, initialState, permalink) {
		return ReactSharedInternals.H.useFormState(action, initialState, permalink);
	};
	exports.useFormStatus = function() {
		return ReactSharedInternals.H.useHostTransitionStatus();
	};
	exports.version = "19.2.7";
}));
//#endregion
//#region ../../node_modules/.pnpm/react-dom@19.2.7_react@19.2.7/node_modules/react-dom/index.js
var require_react_dom = /* @__PURE__ */ __commonJSMin(((exports, module) => {
	function checkDCE() {
		if (typeof __REACT_DEVTOOLS_GLOBAL_HOOK__ === "undefined" || typeof __REACT_DEVTOOLS_GLOBAL_HOOK__.checkDCE !== "function") return;
		try {
			__REACT_DEVTOOLS_GLOBAL_HOOK__.checkDCE(checkDCE);
		} catch (err) {
			console.error(err);
		}
	}
	checkDCE();
	module.exports = require_react_dom_production();
}));
//#endregion
//#region ../../node_modules/.pnpm/react-dom@19.2.7_react@19.2.7/node_modules/react-dom/cjs/react-dom-server.edge.production.js
/**
* @license React
* react-dom-server.edge.production.js
*
* Copyright (c) Meta Platforms, Inc. and affiliates.
*
* This source code is licensed under the MIT license found in the
* LICENSE file in the root directory of this source tree.
*/
var require_react_dom_server_edge_production = /* @__PURE__ */ __commonJSMin(((exports) => {
	var React = require_react();
	var ReactDOM = require_react_dom();
	var REACT_ELEMENT_TYPE = Symbol.for("react.transitional.element");
	var REACT_PORTAL_TYPE = Symbol.for("react.portal");
	var REACT_FRAGMENT_TYPE = Symbol.for("react.fragment");
	var REACT_STRICT_MODE_TYPE = Symbol.for("react.strict_mode");
	var REACT_PROFILER_TYPE = Symbol.for("react.profiler");
	var REACT_CONSUMER_TYPE = Symbol.for("react.consumer");
	var REACT_CONTEXT_TYPE = Symbol.for("react.context");
	var REACT_FORWARD_REF_TYPE = Symbol.for("react.forward_ref");
	var REACT_SUSPENSE_TYPE = Symbol.for("react.suspense");
	var REACT_SUSPENSE_LIST_TYPE = Symbol.for("react.suspense_list");
	var REACT_MEMO_TYPE = Symbol.for("react.memo");
	var REACT_LAZY_TYPE = Symbol.for("react.lazy");
	var REACT_SCOPE_TYPE = Symbol.for("react.scope");
	var REACT_ACTIVITY_TYPE = Symbol.for("react.activity");
	var REACT_LEGACY_HIDDEN_TYPE = Symbol.for("react.legacy_hidden");
	var REACT_MEMO_CACHE_SENTINEL = Symbol.for("react.memo_cache_sentinel");
	var REACT_VIEW_TRANSITION_TYPE = Symbol.for("react.view_transition");
	var MAYBE_ITERATOR_SYMBOL = Symbol.iterator;
	function getIteratorFn(maybeIterable) {
		if (null === maybeIterable || "object" !== typeof maybeIterable) return null;
		maybeIterable = MAYBE_ITERATOR_SYMBOL && maybeIterable[MAYBE_ITERATOR_SYMBOL] || maybeIterable["@@iterator"];
		return "function" === typeof maybeIterable ? maybeIterable : null;
	}
	var isArrayImpl = Array.isArray;
	function murmurhash3_32_gc(key, seed) {
		var remainder = key.length & 3;
		var bytes = key.length - remainder;
		var h1 = seed;
		for (seed = 0; seed < bytes;) {
			var k1 = key.charCodeAt(seed) & 255 | (key.charCodeAt(++seed) & 255) << 8 | (key.charCodeAt(++seed) & 255) << 16 | (key.charCodeAt(++seed) & 255) << 24;
			++seed;
			k1 = 3432918353 * (k1 & 65535) + ((3432918353 * (k1 >>> 16) & 65535) << 16) & 4294967295;
			k1 = k1 << 15 | k1 >>> 17;
			k1 = 461845907 * (k1 & 65535) + ((461845907 * (k1 >>> 16) & 65535) << 16) & 4294967295;
			h1 ^= k1;
			h1 = h1 << 13 | h1 >>> 19;
			h1 = 5 * (h1 & 65535) + ((5 * (h1 >>> 16) & 65535) << 16) & 4294967295;
			h1 = (h1 & 65535) + 27492 + (((h1 >>> 16) + 58964 & 65535) << 16);
		}
		k1 = 0;
		switch (remainder) {
			case 3: k1 ^= (key.charCodeAt(seed + 2) & 255) << 16;
			case 2: k1 ^= (key.charCodeAt(seed + 1) & 255) << 8;
			case 1: k1 ^= key.charCodeAt(seed) & 255, k1 = 3432918353 * (k1 & 65535) + ((3432918353 * (k1 >>> 16) & 65535) << 16) & 4294967295, k1 = k1 << 15 | k1 >>> 17, h1 ^= 461845907 * (k1 & 65535) + ((461845907 * (k1 >>> 16) & 65535) << 16) & 4294967295;
		}
		h1 ^= key.length;
		h1 ^= h1 >>> 16;
		h1 = 2246822507 * (h1 & 65535) + ((2246822507 * (h1 >>> 16) & 65535) << 16) & 4294967295;
		h1 ^= h1 >>> 13;
		h1 = 3266489909 * (h1 & 65535) + ((3266489909 * (h1 >>> 16) & 65535) << 16) & 4294967295;
		return (h1 ^ h1 >>> 16) >>> 0;
	}
	function handleErrorInNextTick(error) {
		setTimeout(function() {
			throw error;
		});
	}
	var LocalPromise = Promise;
	var scheduleMicrotask = "function" === typeof queueMicrotask ? queueMicrotask : function(callback) {
		LocalPromise.resolve(null).then(callback).catch(handleErrorInNextTick);
	};
	var currentView = null;
	var writtenBytes = 0;
	function writeChunk(destination, chunk) {
		if (0 !== chunk.byteLength) if (2048 < chunk.byteLength) 0 < writtenBytes && (destination.enqueue(new Uint8Array(currentView.buffer, 0, writtenBytes)), currentView = /* @__PURE__ */ new Uint8Array(2048), writtenBytes = 0), destination.enqueue(chunk);
		else {
			var allowableBytes = currentView.length - writtenBytes;
			allowableBytes < chunk.byteLength && (0 === allowableBytes ? destination.enqueue(currentView) : (currentView.set(chunk.subarray(0, allowableBytes), writtenBytes), destination.enqueue(currentView), chunk = chunk.subarray(allowableBytes)), currentView = /* @__PURE__ */ new Uint8Array(2048), writtenBytes = 0);
			currentView.set(chunk, writtenBytes);
			writtenBytes += chunk.byteLength;
		}
	}
	function writeChunkAndReturn(destination, chunk) {
		writeChunk(destination, chunk);
		return !0;
	}
	function completeWriting(destination) {
		currentView && 0 < writtenBytes && (destination.enqueue(new Uint8Array(currentView.buffer, 0, writtenBytes)), currentView = null, writtenBytes = 0);
	}
	var textEncoder = new TextEncoder();
	function stringToChunk(content) {
		return textEncoder.encode(content);
	}
	function stringToPrecomputedChunk(content) {
		return textEncoder.encode(content);
	}
	function byteLengthOfChunk(chunk) {
		return chunk.byteLength;
	}
	function closeWithError(destination, error) {
		"function" === typeof destination.error ? destination.error(error) : destination.close();
	}
	var assign = Object.assign;
	var hasOwnProperty = Object.prototype.hasOwnProperty;
	var VALID_ATTRIBUTE_NAME_REGEX = RegExp("^[:A-Z_a-z\\u00C0-\\u00D6\\u00D8-\\u00F6\\u00F8-\\u02FF\\u0370-\\u037D\\u037F-\\u1FFF\\u200C-\\u200D\\u2070-\\u218F\\u2C00-\\u2FEF\\u3001-\\uD7FF\\uF900-\\uFDCF\\uFDF0-\\uFFFD][:A-Z_a-z\\u00C0-\\u00D6\\u00D8-\\u00F6\\u00F8-\\u02FF\\u0370-\\u037D\\u037F-\\u1FFF\\u200C-\\u200D\\u2070-\\u218F\\u2C00-\\u2FEF\\u3001-\\uD7FF\\uF900-\\uFDCF\\uFDF0-\\uFFFD\\-.0-9\\u00B7\\u0300-\\u036F\\u203F-\\u2040]*$");
	var illegalAttributeNameCache = {};
	var validatedAttributeNameCache = {};
	function isAttributeNameSafe(attributeName) {
		if (hasOwnProperty.call(validatedAttributeNameCache, attributeName)) return !0;
		if (hasOwnProperty.call(illegalAttributeNameCache, attributeName)) return !1;
		if (VALID_ATTRIBUTE_NAME_REGEX.test(attributeName)) return validatedAttributeNameCache[attributeName] = !0;
		illegalAttributeNameCache[attributeName] = !0;
		return !1;
	}
	var unitlessNumbers = new Set("animationIterationCount aspectRatio borderImageOutset borderImageSlice borderImageWidth boxFlex boxFlexGroup boxOrdinalGroup columnCount columns flex flexGrow flexPositive flexShrink flexNegative flexOrder gridArea gridRow gridRowEnd gridRowSpan gridRowStart gridColumn gridColumnEnd gridColumnSpan gridColumnStart fontWeight lineClamp lineHeight opacity order orphans scale tabSize widows zIndex zoom fillOpacity floodOpacity stopOpacity strokeDasharray strokeDashoffset strokeMiterlimit strokeOpacity strokeWidth MozAnimationIterationCount MozBoxFlex MozBoxFlexGroup MozLineClamp msAnimationIterationCount msFlex msZoom msFlexGrow msFlexNegative msFlexOrder msFlexPositive msFlexShrink msGridColumn msGridColumnSpan msGridRow msGridRowSpan WebkitAnimationIterationCount WebkitBoxFlex WebKitBoxFlexGroup WebkitBoxOrdinalGroup WebkitColumnCount WebkitColumns WebkitFlex WebkitFlexGrow WebkitFlexPositive WebkitFlexShrink WebkitLineClamp".split(" "));
	var aliases = /* @__PURE__ */ new Map([
		["acceptCharset", "accept-charset"],
		["htmlFor", "for"],
		["httpEquiv", "http-equiv"],
		["crossOrigin", "crossorigin"],
		["accentHeight", "accent-height"],
		["alignmentBaseline", "alignment-baseline"],
		["arabicForm", "arabic-form"],
		["baselineShift", "baseline-shift"],
		["capHeight", "cap-height"],
		["clipPath", "clip-path"],
		["clipRule", "clip-rule"],
		["colorInterpolation", "color-interpolation"],
		["colorInterpolationFilters", "color-interpolation-filters"],
		["colorProfile", "color-profile"],
		["colorRendering", "color-rendering"],
		["dominantBaseline", "dominant-baseline"],
		["enableBackground", "enable-background"],
		["fillOpacity", "fill-opacity"],
		["fillRule", "fill-rule"],
		["floodColor", "flood-color"],
		["floodOpacity", "flood-opacity"],
		["fontFamily", "font-family"],
		["fontSize", "font-size"],
		["fontSizeAdjust", "font-size-adjust"],
		["fontStretch", "font-stretch"],
		["fontStyle", "font-style"],
		["fontVariant", "font-variant"],
		["fontWeight", "font-weight"],
		["glyphName", "glyph-name"],
		["glyphOrientationHorizontal", "glyph-orientation-horizontal"],
		["glyphOrientationVertical", "glyph-orientation-vertical"],
		["horizAdvX", "horiz-adv-x"],
		["horizOriginX", "horiz-origin-x"],
		["imageRendering", "image-rendering"],
		["letterSpacing", "letter-spacing"],
		["lightingColor", "lighting-color"],
		["markerEnd", "marker-end"],
		["markerMid", "marker-mid"],
		["markerStart", "marker-start"],
		["overlinePosition", "overline-position"],
		["overlineThickness", "overline-thickness"],
		["paintOrder", "paint-order"],
		["panose-1", "panose-1"],
		["pointerEvents", "pointer-events"],
		["renderingIntent", "rendering-intent"],
		["shapeRendering", "shape-rendering"],
		["stopColor", "stop-color"],
		["stopOpacity", "stop-opacity"],
		["strikethroughPosition", "strikethrough-position"],
		["strikethroughThickness", "strikethrough-thickness"],
		["strokeDasharray", "stroke-dasharray"],
		["strokeDashoffset", "stroke-dashoffset"],
		["strokeLinecap", "stroke-linecap"],
		["strokeLinejoin", "stroke-linejoin"],
		["strokeMiterlimit", "stroke-miterlimit"],
		["strokeOpacity", "stroke-opacity"],
		["strokeWidth", "stroke-width"],
		["textAnchor", "text-anchor"],
		["textDecoration", "text-decoration"],
		["textRendering", "text-rendering"],
		["transformOrigin", "transform-origin"],
		["underlinePosition", "underline-position"],
		["underlineThickness", "underline-thickness"],
		["unicodeBidi", "unicode-bidi"],
		["unicodeRange", "unicode-range"],
		["unitsPerEm", "units-per-em"],
		["vAlphabetic", "v-alphabetic"],
		["vHanging", "v-hanging"],
		["vIdeographic", "v-ideographic"],
		["vMathematical", "v-mathematical"],
		["vectorEffect", "vector-effect"],
		["vertAdvY", "vert-adv-y"],
		["vertOriginX", "vert-origin-x"],
		["vertOriginY", "vert-origin-y"],
		["wordSpacing", "word-spacing"],
		["writingMode", "writing-mode"],
		["xmlnsXlink", "xmlns:xlink"],
		["xHeight", "x-height"]
	]);
	var matchHtmlRegExp = /["'&<>]/;
	function escapeTextForBrowser(text) {
		if ("boolean" === typeof text || "number" === typeof text || "bigint" === typeof text) return "" + text;
		text = "" + text;
		var match = matchHtmlRegExp.exec(text);
		if (match) {
			var html = "", index, lastIndex = 0;
			for (index = match.index; index < text.length; index++) {
				switch (text.charCodeAt(index)) {
					case 34:
						match = "&quot;";
						break;
					case 38:
						match = "&amp;";
						break;
					case 39:
						match = "&#x27;";
						break;
					case 60:
						match = "&lt;";
						break;
					case 62:
						match = "&gt;";
						break;
					default: continue;
				}
				lastIndex !== index && (html += text.slice(lastIndex, index));
				lastIndex = index + 1;
				html += match;
			}
			text = lastIndex !== index ? html + text.slice(lastIndex, index) : html;
		}
		return text;
	}
	var uppercasePattern = /([A-Z])/g;
	var msPattern = /^ms-/;
	var isJavaScriptProtocol = /^[\u0000-\u001F ]*j[\r\n\t]*a[\r\n\t]*v[\r\n\t]*a[\r\n\t]*s[\r\n\t]*c[\r\n\t]*r[\r\n\t]*i[\r\n\t]*p[\r\n\t]*t[\r\n\t]*:/i;
	function sanitizeURL(url) {
		return isJavaScriptProtocol.test("" + url) ? "javascript:throw new Error('React has blocked a javascript: URL as a security precaution.')" : url;
	}
	var ReactSharedInternals = React.__CLIENT_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE;
	var ReactDOMSharedInternals = ReactDOM.__DOM_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE;
	var sharedNotPendingObject = {
		pending: !1,
		data: null,
		method: null,
		action: null
	};
	var previousDispatcher = ReactDOMSharedInternals.d;
	ReactDOMSharedInternals.d = {
		f: previousDispatcher.f,
		r: previousDispatcher.r,
		D: prefetchDNS,
		C: preconnect,
		L: preload,
		m: preloadModule,
		X: preinitScript,
		S: preinitStyle,
		M: preinitModuleScript
	};
	var PRELOAD_NO_CREDS = [];
	var currentlyFlushingRenderState = null;
	stringToPrecomputedChunk("\"></template>");
	var startInlineScript = stringToPrecomputedChunk("<script");
	var endInlineScript = stringToPrecomputedChunk("<\/script>");
	var startScriptSrc = stringToPrecomputedChunk("<script src=\"");
	var startModuleSrc = stringToPrecomputedChunk("<script type=\"module\" src=\"");
	var scriptNonce = stringToPrecomputedChunk(" nonce=\"");
	var scriptIntegirty = stringToPrecomputedChunk(" integrity=\"");
	var scriptCrossOrigin = stringToPrecomputedChunk(" crossorigin=\"");
	var endAsyncScript = stringToPrecomputedChunk(" async=\"\"><\/script>");
	var startInlineStyle = stringToPrecomputedChunk("<style");
	var scriptRegex = /(<\/|<)(s)(cript)/gi;
	function scriptReplacer(match, prefix, s, suffix) {
		return "" + prefix + ("s" === s ? "\\u0073" : "\\u0053") + suffix;
	}
	var importMapScriptStart = stringToPrecomputedChunk("<script type=\"importmap\">");
	var importMapScriptEnd = stringToPrecomputedChunk("<\/script>");
	function createRenderState(resumableState, nonce, externalRuntimeConfig, importMap, onHeaders, maxHeadersLength) {
		externalRuntimeConfig = "string" === typeof nonce ? nonce : nonce && nonce.script;
		var inlineScriptWithNonce = void 0 === externalRuntimeConfig ? startInlineScript : stringToPrecomputedChunk("<script nonce=\"" + escapeTextForBrowser(externalRuntimeConfig) + "\""), nonceStyle = "string" === typeof nonce ? void 0 : nonce && nonce.style, inlineStyleWithNonce = void 0 === nonceStyle ? startInlineStyle : stringToPrecomputedChunk("<style nonce=\"" + escapeTextForBrowser(nonceStyle) + "\""), idPrefix = resumableState.idPrefix, bootstrapChunks = [], bootstrapScriptContent = resumableState.bootstrapScriptContent, bootstrapScripts = resumableState.bootstrapScripts, bootstrapModules = resumableState.bootstrapModules;
		void 0 !== bootstrapScriptContent && (bootstrapChunks.push(inlineScriptWithNonce), pushCompletedShellIdAttribute(bootstrapChunks, resumableState), bootstrapChunks.push(endOfStartTag, stringToChunk(("" + bootstrapScriptContent).replace(scriptRegex, scriptReplacer)), endInlineScript));
		bootstrapScriptContent = [];
		void 0 !== importMap && (bootstrapScriptContent.push(importMapScriptStart), bootstrapScriptContent.push(stringToChunk(("" + JSON.stringify(importMap)).replace(scriptRegex, scriptReplacer))), bootstrapScriptContent.push(importMapScriptEnd));
		importMap = onHeaders ? {
			preconnects: "",
			fontPreloads: "",
			highImagePreloads: "",
			remainingCapacity: 2 + ("number" === typeof maxHeadersLength ? maxHeadersLength : 2e3)
		} : null;
		onHeaders = {
			placeholderPrefix: stringToPrecomputedChunk(idPrefix + "P:"),
			segmentPrefix: stringToPrecomputedChunk(idPrefix + "S:"),
			boundaryPrefix: stringToPrecomputedChunk(idPrefix + "B:"),
			startInlineScript: inlineScriptWithNonce,
			startInlineStyle: inlineStyleWithNonce,
			preamble: createPreambleState(),
			externalRuntimeScript: null,
			bootstrapChunks,
			importMapChunks: bootstrapScriptContent,
			onHeaders,
			headers: importMap,
			resets: {
				font: {},
				dns: {},
				connect: {
					default: {},
					anonymous: {},
					credentials: {}
				},
				image: {},
				style: {}
			},
			charsetChunks: [],
			viewportChunks: [],
			hoistableChunks: [],
			preconnects: /* @__PURE__ */ new Set(),
			fontPreloads: /* @__PURE__ */ new Set(),
			highImagePreloads: /* @__PURE__ */ new Set(),
			styles: /* @__PURE__ */ new Map(),
			bootstrapScripts: /* @__PURE__ */ new Set(),
			scripts: /* @__PURE__ */ new Set(),
			bulkPreloads: /* @__PURE__ */ new Set(),
			preloads: {
				images: /* @__PURE__ */ new Map(),
				stylesheets: /* @__PURE__ */ new Map(),
				scripts: /* @__PURE__ */ new Map(),
				moduleScripts: /* @__PURE__ */ new Map()
			},
			nonce: {
				script: externalRuntimeConfig,
				style: nonceStyle
			},
			hoistableState: null,
			stylesToHoist: !1
		};
		if (void 0 !== bootstrapScripts) for (importMap = 0; importMap < bootstrapScripts.length; importMap++) idPrefix = bootstrapScripts[importMap], nonceStyle = inlineScriptWithNonce = void 0, inlineStyleWithNonce = {
			rel: "preload",
			as: "script",
			fetchPriority: "low",
			nonce
		}, "string" === typeof idPrefix ? inlineStyleWithNonce.href = maxHeadersLength = idPrefix : (inlineStyleWithNonce.href = maxHeadersLength = idPrefix.src, inlineStyleWithNonce.integrity = nonceStyle = "string" === typeof idPrefix.integrity ? idPrefix.integrity : void 0, inlineStyleWithNonce.crossOrigin = inlineScriptWithNonce = "string" === typeof idPrefix || null == idPrefix.crossOrigin ? void 0 : "use-credentials" === idPrefix.crossOrigin ? "use-credentials" : ""), idPrefix = resumableState, bootstrapScriptContent = maxHeadersLength, idPrefix.scriptResources[bootstrapScriptContent] = null, idPrefix.moduleScriptResources[bootstrapScriptContent] = null, idPrefix = [], pushLinkImpl(idPrefix, inlineStyleWithNonce), onHeaders.bootstrapScripts.add(idPrefix), bootstrapChunks.push(startScriptSrc, stringToChunk(escapeTextForBrowser(maxHeadersLength)), attributeEnd), externalRuntimeConfig && bootstrapChunks.push(scriptNonce, stringToChunk(escapeTextForBrowser(externalRuntimeConfig)), attributeEnd), "string" === typeof nonceStyle && bootstrapChunks.push(scriptIntegirty, stringToChunk(escapeTextForBrowser(nonceStyle)), attributeEnd), "string" === typeof inlineScriptWithNonce && bootstrapChunks.push(scriptCrossOrigin, stringToChunk(escapeTextForBrowser(inlineScriptWithNonce)), attributeEnd), pushCompletedShellIdAttribute(bootstrapChunks, resumableState), bootstrapChunks.push(endAsyncScript);
		if (void 0 !== bootstrapModules) for (nonce = 0; nonce < bootstrapModules.length; nonce++) nonceStyle = bootstrapModules[nonce], maxHeadersLength = importMap = void 0, inlineScriptWithNonce = {
			rel: "modulepreload",
			fetchPriority: "low",
			nonce: externalRuntimeConfig
		}, "string" === typeof nonceStyle ? inlineScriptWithNonce.href = bootstrapScripts = nonceStyle : (inlineScriptWithNonce.href = bootstrapScripts = nonceStyle.src, inlineScriptWithNonce.integrity = maxHeadersLength = "string" === typeof nonceStyle.integrity ? nonceStyle.integrity : void 0, inlineScriptWithNonce.crossOrigin = importMap = "string" === typeof nonceStyle || null == nonceStyle.crossOrigin ? void 0 : "use-credentials" === nonceStyle.crossOrigin ? "use-credentials" : ""), nonceStyle = resumableState, inlineStyleWithNonce = bootstrapScripts, nonceStyle.scriptResources[inlineStyleWithNonce] = null, nonceStyle.moduleScriptResources[inlineStyleWithNonce] = null, nonceStyle = [], pushLinkImpl(nonceStyle, inlineScriptWithNonce), onHeaders.bootstrapScripts.add(nonceStyle), bootstrapChunks.push(startModuleSrc, stringToChunk(escapeTextForBrowser(bootstrapScripts)), attributeEnd), externalRuntimeConfig && bootstrapChunks.push(scriptNonce, stringToChunk(escapeTextForBrowser(externalRuntimeConfig)), attributeEnd), "string" === typeof maxHeadersLength && bootstrapChunks.push(scriptIntegirty, stringToChunk(escapeTextForBrowser(maxHeadersLength)), attributeEnd), "string" === typeof importMap && bootstrapChunks.push(scriptCrossOrigin, stringToChunk(escapeTextForBrowser(importMap)), attributeEnd), pushCompletedShellIdAttribute(bootstrapChunks, resumableState), bootstrapChunks.push(endAsyncScript);
		return onHeaders;
	}
	function createResumableState(identifierPrefix, externalRuntimeConfig, bootstrapScriptContent, bootstrapScripts, bootstrapModules) {
		return {
			idPrefix: void 0 === identifierPrefix ? "" : identifierPrefix,
			nextFormID: 0,
			streamingFormat: 0,
			bootstrapScriptContent,
			bootstrapScripts,
			bootstrapModules,
			instructions: 0,
			hasBody: !1,
			hasHtml: !1,
			unknownResources: {},
			dnsResources: {},
			connectResources: {
				default: {},
				anonymous: {},
				credentials: {}
			},
			imageResources: {},
			styleResources: {},
			scriptResources: {},
			moduleUnknownResources: {},
			moduleScriptResources: {}
		};
	}
	function createPreambleState() {
		return {
			htmlChunks: null,
			headChunks: null,
			bodyChunks: null
		};
	}
	function createFormatContext(insertionMode, selectedValue, tagScope, viewTransition) {
		return {
			insertionMode,
			selectedValue,
			tagScope,
			viewTransition
		};
	}
	function createRootFormatContext(namespaceURI) {
		return createFormatContext("http://www.w3.org/2000/svg" === namespaceURI ? 4 : "http://www.w3.org/1998/Math/MathML" === namespaceURI ? 5 : 0, null, 0, null);
	}
	function getChildFormatContext(parentContext, type, props) {
		var subtreeScope = parentContext.tagScope & -25;
		switch (type) {
			case "noscript": return createFormatContext(2, null, subtreeScope | 1, null);
			case "select": return createFormatContext(2, null != props.value ? props.value : props.defaultValue, subtreeScope, null);
			case "svg": return createFormatContext(4, null, subtreeScope, null);
			case "picture": return createFormatContext(2, null, subtreeScope | 2, null);
			case "math": return createFormatContext(5, null, subtreeScope, null);
			case "foreignObject": return createFormatContext(2, null, subtreeScope, null);
			case "table": return createFormatContext(6, null, subtreeScope, null);
			case "thead":
			case "tbody":
			case "tfoot": return createFormatContext(7, null, subtreeScope, null);
			case "colgroup": return createFormatContext(9, null, subtreeScope, null);
			case "tr": return createFormatContext(8, null, subtreeScope, null);
			case "head":
				if (2 > parentContext.insertionMode) return createFormatContext(3, null, subtreeScope, null);
				break;
			case "html": if (0 === parentContext.insertionMode) return createFormatContext(1, null, subtreeScope, null);
		}
		return 6 <= parentContext.insertionMode || 2 > parentContext.insertionMode ? createFormatContext(2, null, subtreeScope, null) : parentContext.tagScope !== subtreeScope ? createFormatContext(parentContext.insertionMode, parentContext.selectedValue, subtreeScope, null) : parentContext;
	}
	function getSuspenseViewTransition(parentViewTransition) {
		return null === parentViewTransition ? null : {
			update: parentViewTransition.update,
			enter: "none",
			exit: "none",
			share: parentViewTransition.update,
			name: parentViewTransition.autoName,
			autoName: parentViewTransition.autoName,
			nameIdx: 0
		};
	}
	function getSuspenseFallbackFormatContext(resumableState, parentContext) {
		parentContext.tagScope & 32 && (resumableState.instructions |= 128);
		return createFormatContext(parentContext.insertionMode, parentContext.selectedValue, parentContext.tagScope | 12, getSuspenseViewTransition(parentContext.viewTransition));
	}
	function getSuspenseContentFormatContext(resumableState, parentContext) {
		resumableState = getSuspenseViewTransition(parentContext.viewTransition);
		var subtreeScope = parentContext.tagScope | 16;
		null !== resumableState && "none" !== resumableState.share && (subtreeScope |= 64);
		return createFormatContext(parentContext.insertionMode, parentContext.selectedValue, subtreeScope, resumableState);
	}
	var textSeparator = stringToPrecomputedChunk("<!-- -->");
	function pushTextInstance(target, text, renderState, textEmbedded) {
		if ("" === text) return textEmbedded;
		textEmbedded && target.push(textSeparator);
		target.push(stringToChunk(escapeTextForBrowser(text)));
		return !0;
	}
	var styleNameCache = /* @__PURE__ */ new Map();
	var styleAttributeStart = stringToPrecomputedChunk(" style=\"");
	var styleAssign = stringToPrecomputedChunk(":");
	var styleSeparator = stringToPrecomputedChunk(";");
	function pushStyleAttribute(target, style) {
		if ("object" !== typeof style) throw Error("The `style` prop expects a mapping from style properties to values, not a string. For example, style={{marginRight: spacing + 'em'}} when using JSX.");
		var isFirst = !0, styleName;
		for (styleName in style) if (hasOwnProperty.call(style, styleName)) {
			var styleValue = style[styleName];
			if (null != styleValue && "boolean" !== typeof styleValue && "" !== styleValue) {
				if (0 === styleName.indexOf("--")) {
					var nameChunk = stringToChunk(escapeTextForBrowser(styleName));
					styleValue = stringToChunk(escapeTextForBrowser(("" + styleValue).trim()));
				} else nameChunk = styleNameCache.get(styleName), void 0 === nameChunk && (nameChunk = stringToPrecomputedChunk(escapeTextForBrowser(styleName.replace(uppercasePattern, "-$1").toLowerCase().replace(msPattern, "-ms-"))), styleNameCache.set(styleName, nameChunk)), styleValue = "number" === typeof styleValue ? 0 === styleValue || unitlessNumbers.has(styleName) ? stringToChunk("" + styleValue) : stringToChunk(styleValue + "px") : stringToChunk(escapeTextForBrowser(("" + styleValue).trim()));
				isFirst ? (isFirst = !1, target.push(styleAttributeStart, nameChunk, styleAssign, styleValue)) : target.push(styleSeparator, nameChunk, styleAssign, styleValue);
			}
		}
		isFirst || target.push(attributeEnd);
	}
	var attributeSeparator = stringToPrecomputedChunk(" ");
	var attributeAssign = stringToPrecomputedChunk("=\"");
	var attributeEnd = stringToPrecomputedChunk("\"");
	var attributeEmptyString = stringToPrecomputedChunk("=\"\"");
	function pushBooleanAttribute(target, name, value) {
		value && "function" !== typeof value && "symbol" !== typeof value && target.push(attributeSeparator, stringToChunk(name), attributeEmptyString);
	}
	function pushStringAttribute(target, name, value) {
		"function" !== typeof value && "symbol" !== typeof value && "boolean" !== typeof value && target.push(attributeSeparator, stringToChunk(name), attributeAssign, stringToChunk(escapeTextForBrowser(value)), attributeEnd);
	}
	var actionJavaScriptURL = stringToPrecomputedChunk(escapeTextForBrowser("javascript:throw new Error('React form unexpectedly submitted.')"));
	var startHiddenInputChunk = stringToPrecomputedChunk("<input type=\"hidden\"");
	function pushAdditionalFormField(value, key) {
		this.push(startHiddenInputChunk);
		validateAdditionalFormField(value);
		pushStringAttribute(this, "name", key);
		pushStringAttribute(this, "value", value);
		this.push(endOfStartTagSelfClosing);
	}
	function validateAdditionalFormField(value) {
		if ("string" !== typeof value) throw Error("File/Blob fields are not yet supported in progressive forms. Will fallback to client hydration.");
	}
	function getCustomFormFields(resumableState, formAction) {
		if ("function" === typeof formAction.$$FORM_ACTION) {
			var id = resumableState.nextFormID++;
			resumableState = resumableState.idPrefix + id;
			try {
				var customFields = formAction.$$FORM_ACTION(resumableState);
				if (customFields) customFields.data?.forEach(validateAdditionalFormField);
				return customFields;
			} catch (x) {
				if ("object" === typeof x && null !== x && "function" === typeof x.then) throw x;
			}
		}
		return null;
	}
	function pushFormActionAttribute(target, resumableState, renderState, formAction, formEncType, formMethod, formTarget, name) {
		var formData = null;
		if ("function" === typeof formAction) {
			var customFields = getCustomFormFields(resumableState, formAction);
			null !== customFields ? (name = customFields.name, formAction = customFields.action || "", formEncType = customFields.encType, formMethod = customFields.method, formTarget = customFields.target, formData = customFields.data) : (target.push(attributeSeparator, stringToChunk("formAction"), attributeAssign, actionJavaScriptURL, attributeEnd), formTarget = formMethod = formEncType = formAction = name = null, injectFormReplayingRuntime(resumableState, renderState));
		}
		null != name && pushAttribute(target, "name", name);
		null != formAction && pushAttribute(target, "formAction", formAction);
		null != formEncType && pushAttribute(target, "formEncType", formEncType);
		null != formMethod && pushAttribute(target, "formMethod", formMethod);
		null != formTarget && pushAttribute(target, "formTarget", formTarget);
		return formData;
	}
	function pushAttribute(target, name, value) {
		switch (name) {
			case "className":
				pushStringAttribute(target, "class", value);
				break;
			case "tabIndex":
				pushStringAttribute(target, "tabindex", value);
				break;
			case "dir":
			case "role":
			case "viewBox":
			case "width":
			case "height":
				pushStringAttribute(target, name, value);
				break;
			case "style":
				pushStyleAttribute(target, value);
				break;
			case "src":
			case "href": if ("" === value) break;
			case "action":
			case "formAction":
				if (null == value || "function" === typeof value || "symbol" === typeof value || "boolean" === typeof value) break;
				value = sanitizeURL("" + value);
				target.push(attributeSeparator, stringToChunk(name), attributeAssign, stringToChunk(escapeTextForBrowser(value)), attributeEnd);
				break;
			case "defaultValue":
			case "defaultChecked":
			case "innerHTML":
			case "suppressContentEditableWarning":
			case "suppressHydrationWarning":
			case "ref": break;
			case "autoFocus":
			case "multiple":
			case "muted":
				pushBooleanAttribute(target, name.toLowerCase(), value);
				break;
			case "xlinkHref":
				if ("function" === typeof value || "symbol" === typeof value || "boolean" === typeof value) break;
				value = sanitizeURL("" + value);
				target.push(attributeSeparator, stringToChunk("xlink:href"), attributeAssign, stringToChunk(escapeTextForBrowser(value)), attributeEnd);
				break;
			case "contentEditable":
			case "spellCheck":
			case "draggable":
			case "value":
			case "autoReverse":
			case "externalResourcesRequired":
			case "focusable":
			case "preserveAlpha":
				"function" !== typeof value && "symbol" !== typeof value && target.push(attributeSeparator, stringToChunk(name), attributeAssign, stringToChunk(escapeTextForBrowser(value)), attributeEnd);
				break;
			case "inert":
			case "allowFullScreen":
			case "async":
			case "autoPlay":
			case "controls":
			case "default":
			case "defer":
			case "disabled":
			case "disablePictureInPicture":
			case "disableRemotePlayback":
			case "formNoValidate":
			case "hidden":
			case "loop":
			case "noModule":
			case "noValidate":
			case "open":
			case "playsInline":
			case "readOnly":
			case "required":
			case "reversed":
			case "scoped":
			case "seamless":
			case "itemScope":
				value && "function" !== typeof value && "symbol" !== typeof value && target.push(attributeSeparator, stringToChunk(name), attributeEmptyString);
				break;
			case "capture":
			case "download":
				!0 === value ? target.push(attributeSeparator, stringToChunk(name), attributeEmptyString) : !1 !== value && "function" !== typeof value && "symbol" !== typeof value && target.push(attributeSeparator, stringToChunk(name), attributeAssign, stringToChunk(escapeTextForBrowser(value)), attributeEnd);
				break;
			case "cols":
			case "rows":
			case "size":
			case "span":
				"function" !== typeof value && "symbol" !== typeof value && !isNaN(value) && 1 <= value && target.push(attributeSeparator, stringToChunk(name), attributeAssign, stringToChunk(escapeTextForBrowser(value)), attributeEnd);
				break;
			case "rowSpan":
			case "start":
				"function" === typeof value || "symbol" === typeof value || isNaN(value) || target.push(attributeSeparator, stringToChunk(name), attributeAssign, stringToChunk(escapeTextForBrowser(value)), attributeEnd);
				break;
			case "xlinkActuate":
				pushStringAttribute(target, "xlink:actuate", value);
				break;
			case "xlinkArcrole":
				pushStringAttribute(target, "xlink:arcrole", value);
				break;
			case "xlinkRole":
				pushStringAttribute(target, "xlink:role", value);
				break;
			case "xlinkShow":
				pushStringAttribute(target, "xlink:show", value);
				break;
			case "xlinkTitle":
				pushStringAttribute(target, "xlink:title", value);
				break;
			case "xlinkType":
				pushStringAttribute(target, "xlink:type", value);
				break;
			case "xmlBase":
				pushStringAttribute(target, "xml:base", value);
				break;
			case "xmlLang":
				pushStringAttribute(target, "xml:lang", value);
				break;
			case "xmlSpace":
				pushStringAttribute(target, "xml:space", value);
				break;
			default: if (!(2 < name.length) || "o" !== name[0] && "O" !== name[0] || "n" !== name[1] && "N" !== name[1]) {
				if (name = aliases.get(name) || name, isAttributeNameSafe(name)) {
					switch (typeof value) {
						case "function":
						case "symbol": return;
						case "boolean":
							var prefix$8 = name.toLowerCase().slice(0, 5);
							if ("data-" !== prefix$8 && "aria-" !== prefix$8) return;
					}
					target.push(attributeSeparator, stringToChunk(name), attributeAssign, stringToChunk(escapeTextForBrowser(value)), attributeEnd);
				}
			}
		}
	}
	var endOfStartTag = stringToPrecomputedChunk(">");
	var endOfStartTagSelfClosing = stringToPrecomputedChunk("/>");
	function pushInnerHTML(target, innerHTML, children) {
		if (null != innerHTML) {
			if (null != children) throw Error("Can only set one of `children` or `props.dangerouslySetInnerHTML`.");
			if ("object" !== typeof innerHTML || !("__html" in innerHTML)) throw Error("`props.dangerouslySetInnerHTML` must be in the form `{__html: ...}`. Please visit https://react.dev/link/dangerously-set-inner-html for more information.");
			innerHTML = innerHTML.__html;
			null !== innerHTML && void 0 !== innerHTML && target.push(stringToChunk("" + innerHTML));
		}
	}
	function flattenOptionChildren(children) {
		var content = "";
		React.Children.forEach(children, function(child) {
			null != child && (content += child);
		});
		return content;
	}
	var selectedMarkerAttribute = stringToPrecomputedChunk(" selected=\"\"");
	var formReplayingRuntimeScript = stringToPrecomputedChunk("addEventListener(\"submit\",function(a){if(!a.defaultPrevented){var c=a.target,d=a.submitter,e=c.action,b=d;if(d){var f=d.getAttribute(\"formAction\");null!=f&&(e=f,b=null)}\"javascript:throw new Error('React form unexpectedly submitted.')\"===e&&(a.preventDefault(),b?(a=document.createElement(\"input\"),a.name=b.name,a.value=b.value,b.parentNode.insertBefore(a,b),b=new FormData(c),a.parentNode.removeChild(a)):b=new FormData(c),a=c.ownerDocument||c,(a.$$reactFormReplay=a.$$reactFormReplay||[]).push(c,d,b))}});");
	function injectFormReplayingRuntime(resumableState, renderState) {
		if (0 === (resumableState.instructions & 16)) {
			resumableState.instructions |= 16;
			var preamble = renderState.preamble, bootstrapChunks = renderState.bootstrapChunks;
			(preamble.htmlChunks || preamble.headChunks) && 0 === bootstrapChunks.length ? (bootstrapChunks.push(renderState.startInlineScript), pushCompletedShellIdAttribute(bootstrapChunks, resumableState), bootstrapChunks.push(endOfStartTag, formReplayingRuntimeScript, endInlineScript)) : bootstrapChunks.unshift(renderState.startInlineScript, endOfStartTag, formReplayingRuntimeScript, endInlineScript);
		}
	}
	var formStateMarkerIsMatching = stringToPrecomputedChunk("<!--F!-->");
	var formStateMarkerIsNotMatching = stringToPrecomputedChunk("<!--F-->");
	function pushLinkImpl(target, props) {
		target.push(startChunkForTag("link"));
		for (var propKey in props) if (hasOwnProperty.call(props, propKey)) {
			var propValue = props[propKey];
			if (null != propValue) switch (propKey) {
				case "children":
				case "dangerouslySetInnerHTML": throw Error("link is a self-closing tag and must neither have `children` nor use `dangerouslySetInnerHTML`.");
				default: pushAttribute(target, propKey, propValue);
			}
		}
		target.push(endOfStartTagSelfClosing);
		return null;
	}
	var styleRegex = /(<\/|<)(s)(tyle)/gi;
	function styleReplacer(match, prefix, s, suffix) {
		return "" + prefix + ("s" === s ? "\\73 " : "\\53 ") + suffix;
	}
	function pushSelfClosing(target, props, tag) {
		target.push(startChunkForTag(tag));
		for (var propKey in props) if (hasOwnProperty.call(props, propKey)) {
			var propValue = props[propKey];
			if (null != propValue) switch (propKey) {
				case "children":
				case "dangerouslySetInnerHTML": throw Error(tag + " is a self-closing tag and must neither have `children` nor use `dangerouslySetInnerHTML`.");
				default: pushAttribute(target, propKey, propValue);
			}
		}
		target.push(endOfStartTagSelfClosing);
		return null;
	}
	function pushTitleImpl(target, props) {
		target.push(startChunkForTag("title"));
		var children = null, innerHTML = null, propKey;
		for (propKey in props) if (hasOwnProperty.call(props, propKey)) {
			var propValue = props[propKey];
			if (null != propValue) switch (propKey) {
				case "children":
					children = propValue;
					break;
				case "dangerouslySetInnerHTML":
					innerHTML = propValue;
					break;
				default: pushAttribute(target, propKey, propValue);
			}
		}
		target.push(endOfStartTag);
		props = Array.isArray(children) ? 2 > children.length ? children[0] : null : children;
		"function" !== typeof props && "symbol" !== typeof props && null !== props && void 0 !== props && target.push(stringToChunk(escapeTextForBrowser("" + props)));
		pushInnerHTML(target, innerHTML, children);
		target.push(endChunkForTag("title"));
		return null;
	}
	var headPreambleContributionChunk = stringToPrecomputedChunk("<!--head-->");
	var bodyPreambleContributionChunk = stringToPrecomputedChunk("<!--body-->");
	var htmlPreambleContributionChunk = stringToPrecomputedChunk("<!--html-->");
	function pushScriptImpl(target, props) {
		target.push(startChunkForTag("script"));
		var children = null, innerHTML = null, propKey;
		for (propKey in props) if (hasOwnProperty.call(props, propKey)) {
			var propValue = props[propKey];
			if (null != propValue) switch (propKey) {
				case "children":
					children = propValue;
					break;
				case "dangerouslySetInnerHTML":
					innerHTML = propValue;
					break;
				default: pushAttribute(target, propKey, propValue);
			}
		}
		target.push(endOfStartTag);
		pushInnerHTML(target, innerHTML, children);
		"string" === typeof children && target.push(stringToChunk(("" + children).replace(scriptRegex, scriptReplacer)));
		target.push(endChunkForTag("script"));
		return null;
	}
	function pushStartSingletonElement(target, props, tag) {
		target.push(startChunkForTag(tag));
		var innerHTML = tag = null, propKey;
		for (propKey in props) if (hasOwnProperty.call(props, propKey)) {
			var propValue = props[propKey];
			if (null != propValue) switch (propKey) {
				case "children":
					tag = propValue;
					break;
				case "dangerouslySetInnerHTML":
					innerHTML = propValue;
					break;
				default: pushAttribute(target, propKey, propValue);
			}
		}
		target.push(endOfStartTag);
		pushInnerHTML(target, innerHTML, tag);
		return tag;
	}
	function pushStartGenericElement(target, props, tag) {
		target.push(startChunkForTag(tag));
		var innerHTML = tag = null, propKey;
		for (propKey in props) if (hasOwnProperty.call(props, propKey)) {
			var propValue = props[propKey];
			if (null != propValue) switch (propKey) {
				case "children":
					tag = propValue;
					break;
				case "dangerouslySetInnerHTML":
					innerHTML = propValue;
					break;
				default: pushAttribute(target, propKey, propValue);
			}
		}
		target.push(endOfStartTag);
		pushInnerHTML(target, innerHTML, tag);
		return "string" === typeof tag ? (target.push(stringToChunk(escapeTextForBrowser(tag))), null) : tag;
	}
	var leadingNewline = stringToPrecomputedChunk("\n");
	var VALID_TAG_REGEX = /^[a-zA-Z][a-zA-Z:_\.\-\d]*$/;
	var validatedTagCache = /* @__PURE__ */ new Map();
	function startChunkForTag(tag) {
		var tagStartChunk = validatedTagCache.get(tag);
		if (void 0 === tagStartChunk) {
			if (!VALID_TAG_REGEX.test(tag)) throw Error("Invalid tag: " + tag);
			tagStartChunk = stringToPrecomputedChunk("<" + tag);
			validatedTagCache.set(tag, tagStartChunk);
		}
		return tagStartChunk;
	}
	var doctypeChunk = stringToPrecomputedChunk("<!DOCTYPE html>");
	function pushStartInstance(target$jscomp$0, type, props, resumableState, renderState, preambleState, hoistableState, formatContext, textEmbedded) {
		switch (type) {
			case "div":
			case "span":
			case "svg":
			case "path": break;
			case "a":
				target$jscomp$0.push(startChunkForTag("a"));
				var children = null, innerHTML = null, propKey;
				for (propKey in props) if (hasOwnProperty.call(props, propKey)) {
					var propValue = props[propKey];
					if (null != propValue) switch (propKey) {
						case "children":
							children = propValue;
							break;
						case "dangerouslySetInnerHTML":
							innerHTML = propValue;
							break;
						case "href":
							"" === propValue ? pushStringAttribute(target$jscomp$0, "href", "") : pushAttribute(target$jscomp$0, propKey, propValue);
							break;
						default: pushAttribute(target$jscomp$0, propKey, propValue);
					}
				}
				target$jscomp$0.push(endOfStartTag);
				pushInnerHTML(target$jscomp$0, innerHTML, children);
				if ("string" === typeof children) {
					target$jscomp$0.push(stringToChunk(escapeTextForBrowser(children)));
					var JSCompiler_inline_result = null;
				} else JSCompiler_inline_result = children;
				return JSCompiler_inline_result;
			case "g":
			case "p":
			case "li": break;
			case "select":
				target$jscomp$0.push(startChunkForTag("select"));
				var children$jscomp$0 = null, innerHTML$jscomp$0 = null, propKey$jscomp$0;
				for (propKey$jscomp$0 in props) if (hasOwnProperty.call(props, propKey$jscomp$0)) {
					var propValue$jscomp$0 = props[propKey$jscomp$0];
					if (null != propValue$jscomp$0) switch (propKey$jscomp$0) {
						case "children":
							children$jscomp$0 = propValue$jscomp$0;
							break;
						case "dangerouslySetInnerHTML":
							innerHTML$jscomp$0 = propValue$jscomp$0;
							break;
						case "defaultValue":
						case "value": break;
						default: pushAttribute(target$jscomp$0, propKey$jscomp$0, propValue$jscomp$0);
					}
				}
				target$jscomp$0.push(endOfStartTag);
				pushInnerHTML(target$jscomp$0, innerHTML$jscomp$0, children$jscomp$0);
				return children$jscomp$0;
			case "option":
				var selectedValue = formatContext.selectedValue;
				target$jscomp$0.push(startChunkForTag("option"));
				var children$jscomp$1 = null, value = null, selected = null, innerHTML$jscomp$1 = null, propKey$jscomp$1;
				for (propKey$jscomp$1 in props) if (hasOwnProperty.call(props, propKey$jscomp$1)) {
					var propValue$jscomp$1 = props[propKey$jscomp$1];
					if (null != propValue$jscomp$1) switch (propKey$jscomp$1) {
						case "children":
							children$jscomp$1 = propValue$jscomp$1;
							break;
						case "selected":
							selected = propValue$jscomp$1;
							break;
						case "dangerouslySetInnerHTML":
							innerHTML$jscomp$1 = propValue$jscomp$1;
							break;
						case "value": value = propValue$jscomp$1;
						default: pushAttribute(target$jscomp$0, propKey$jscomp$1, propValue$jscomp$1);
					}
				}
				if (null != selectedValue) {
					var stringValue = null !== value ? "" + value : flattenOptionChildren(children$jscomp$1);
					if (isArrayImpl(selectedValue)) {
						for (var i = 0; i < selectedValue.length; i++) if ("" + selectedValue[i] === stringValue) {
							target$jscomp$0.push(selectedMarkerAttribute);
							break;
						}
					} else "" + selectedValue === stringValue && target$jscomp$0.push(selectedMarkerAttribute);
				} else selected && target$jscomp$0.push(selectedMarkerAttribute);
				target$jscomp$0.push(endOfStartTag);
				pushInnerHTML(target$jscomp$0, innerHTML$jscomp$1, children$jscomp$1);
				return children$jscomp$1;
			case "textarea":
				target$jscomp$0.push(startChunkForTag("textarea"));
				var value$jscomp$0 = null, defaultValue = null, children$jscomp$2 = null, propKey$jscomp$2;
				for (propKey$jscomp$2 in props) if (hasOwnProperty.call(props, propKey$jscomp$2)) {
					var propValue$jscomp$2 = props[propKey$jscomp$2];
					if (null != propValue$jscomp$2) switch (propKey$jscomp$2) {
						case "children":
							children$jscomp$2 = propValue$jscomp$2;
							break;
						case "value":
							value$jscomp$0 = propValue$jscomp$2;
							break;
						case "defaultValue":
							defaultValue = propValue$jscomp$2;
							break;
						case "dangerouslySetInnerHTML": throw Error("`dangerouslySetInnerHTML` does not make sense on <textarea>.");
						default: pushAttribute(target$jscomp$0, propKey$jscomp$2, propValue$jscomp$2);
					}
				}
				null === value$jscomp$0 && null !== defaultValue && (value$jscomp$0 = defaultValue);
				target$jscomp$0.push(endOfStartTag);
				if (null != children$jscomp$2) {
					if (null != value$jscomp$0) throw Error("If you supply `defaultValue` on a <textarea>, do not pass children.");
					if (isArrayImpl(children$jscomp$2)) {
						if (1 < children$jscomp$2.length) throw Error("<textarea> can only have at most one child.");
						value$jscomp$0 = "" + children$jscomp$2[0];
					}
					value$jscomp$0 = "" + children$jscomp$2;
				}
				"string" === typeof value$jscomp$0 && "\n" === value$jscomp$0[0] && target$jscomp$0.push(leadingNewline);
				null !== value$jscomp$0 && target$jscomp$0.push(stringToChunk(escapeTextForBrowser("" + value$jscomp$0)));
				return null;
			case "input":
				target$jscomp$0.push(startChunkForTag("input"));
				var name = null, formAction = null, formEncType = null, formMethod = null, formTarget = null, value$jscomp$1 = null, defaultValue$jscomp$0 = null, checked = null, defaultChecked = null, propKey$jscomp$3;
				for (propKey$jscomp$3 in props) if (hasOwnProperty.call(props, propKey$jscomp$3)) {
					var propValue$jscomp$3 = props[propKey$jscomp$3];
					if (null != propValue$jscomp$3) switch (propKey$jscomp$3) {
						case "children":
						case "dangerouslySetInnerHTML": throw Error("input is a self-closing tag and must neither have `children` nor use `dangerouslySetInnerHTML`.");
						case "name":
							name = propValue$jscomp$3;
							break;
						case "formAction":
							formAction = propValue$jscomp$3;
							break;
						case "formEncType":
							formEncType = propValue$jscomp$3;
							break;
						case "formMethod":
							formMethod = propValue$jscomp$3;
							break;
						case "formTarget":
							formTarget = propValue$jscomp$3;
							break;
						case "defaultChecked":
							defaultChecked = propValue$jscomp$3;
							break;
						case "defaultValue":
							defaultValue$jscomp$0 = propValue$jscomp$3;
							break;
						case "checked":
							checked = propValue$jscomp$3;
							break;
						case "value":
							value$jscomp$1 = propValue$jscomp$3;
							break;
						default: pushAttribute(target$jscomp$0, propKey$jscomp$3, propValue$jscomp$3);
					}
				}
				var formData = pushFormActionAttribute(target$jscomp$0, resumableState, renderState, formAction, formEncType, formMethod, formTarget, name);
				null !== checked ? pushBooleanAttribute(target$jscomp$0, "checked", checked) : null !== defaultChecked && pushBooleanAttribute(target$jscomp$0, "checked", defaultChecked);
				null !== value$jscomp$1 ? pushAttribute(target$jscomp$0, "value", value$jscomp$1) : null !== defaultValue$jscomp$0 && pushAttribute(target$jscomp$0, "value", defaultValue$jscomp$0);
				target$jscomp$0.push(endOfStartTagSelfClosing);
				formData?.forEach(pushAdditionalFormField, target$jscomp$0);
				return null;
			case "button":
				target$jscomp$0.push(startChunkForTag("button"));
				var children$jscomp$3 = null, innerHTML$jscomp$2 = null, name$jscomp$0 = null, formAction$jscomp$0 = null, formEncType$jscomp$0 = null, formMethod$jscomp$0 = null, formTarget$jscomp$0 = null, propKey$jscomp$4;
				for (propKey$jscomp$4 in props) if (hasOwnProperty.call(props, propKey$jscomp$4)) {
					var propValue$jscomp$4 = props[propKey$jscomp$4];
					if (null != propValue$jscomp$4) switch (propKey$jscomp$4) {
						case "children":
							children$jscomp$3 = propValue$jscomp$4;
							break;
						case "dangerouslySetInnerHTML":
							innerHTML$jscomp$2 = propValue$jscomp$4;
							break;
						case "name":
							name$jscomp$0 = propValue$jscomp$4;
							break;
						case "formAction":
							formAction$jscomp$0 = propValue$jscomp$4;
							break;
						case "formEncType":
							formEncType$jscomp$0 = propValue$jscomp$4;
							break;
						case "formMethod":
							formMethod$jscomp$0 = propValue$jscomp$4;
							break;
						case "formTarget":
							formTarget$jscomp$0 = propValue$jscomp$4;
							break;
						default: pushAttribute(target$jscomp$0, propKey$jscomp$4, propValue$jscomp$4);
					}
				}
				var formData$jscomp$0 = pushFormActionAttribute(target$jscomp$0, resumableState, renderState, formAction$jscomp$0, formEncType$jscomp$0, formMethod$jscomp$0, formTarget$jscomp$0, name$jscomp$0);
				target$jscomp$0.push(endOfStartTag);
				formData$jscomp$0?.forEach(pushAdditionalFormField, target$jscomp$0);
				pushInnerHTML(target$jscomp$0, innerHTML$jscomp$2, children$jscomp$3);
				if ("string" === typeof children$jscomp$3) {
					target$jscomp$0.push(stringToChunk(escapeTextForBrowser(children$jscomp$3)));
					var JSCompiler_inline_result$jscomp$0 = null;
				} else JSCompiler_inline_result$jscomp$0 = children$jscomp$3;
				return JSCompiler_inline_result$jscomp$0;
			case "form":
				target$jscomp$0.push(startChunkForTag("form"));
				var children$jscomp$4 = null, innerHTML$jscomp$3 = null, formAction$jscomp$1 = null, formEncType$jscomp$1 = null, formMethod$jscomp$1 = null, formTarget$jscomp$1 = null, propKey$jscomp$5;
				for (propKey$jscomp$5 in props) if (hasOwnProperty.call(props, propKey$jscomp$5)) {
					var propValue$jscomp$5 = props[propKey$jscomp$5];
					if (null != propValue$jscomp$5) switch (propKey$jscomp$5) {
						case "children":
							children$jscomp$4 = propValue$jscomp$5;
							break;
						case "dangerouslySetInnerHTML":
							innerHTML$jscomp$3 = propValue$jscomp$5;
							break;
						case "action":
							formAction$jscomp$1 = propValue$jscomp$5;
							break;
						case "encType":
							formEncType$jscomp$1 = propValue$jscomp$5;
							break;
						case "method":
							formMethod$jscomp$1 = propValue$jscomp$5;
							break;
						case "target":
							formTarget$jscomp$1 = propValue$jscomp$5;
							break;
						default: pushAttribute(target$jscomp$0, propKey$jscomp$5, propValue$jscomp$5);
					}
				}
				var formData$jscomp$1 = null, formActionName = null;
				if ("function" === typeof formAction$jscomp$1) {
					var customFields = getCustomFormFields(resumableState, formAction$jscomp$1);
					null !== customFields ? (formAction$jscomp$1 = customFields.action || "", formEncType$jscomp$1 = customFields.encType, formMethod$jscomp$1 = customFields.method, formTarget$jscomp$1 = customFields.target, formData$jscomp$1 = customFields.data, formActionName = customFields.name) : (target$jscomp$0.push(attributeSeparator, stringToChunk("action"), attributeAssign, actionJavaScriptURL, attributeEnd), formTarget$jscomp$1 = formMethod$jscomp$1 = formEncType$jscomp$1 = formAction$jscomp$1 = null, injectFormReplayingRuntime(resumableState, renderState));
				}
				null != formAction$jscomp$1 && pushAttribute(target$jscomp$0, "action", formAction$jscomp$1);
				null != formEncType$jscomp$1 && pushAttribute(target$jscomp$0, "encType", formEncType$jscomp$1);
				null != formMethod$jscomp$1 && pushAttribute(target$jscomp$0, "method", formMethod$jscomp$1);
				null != formTarget$jscomp$1 && pushAttribute(target$jscomp$0, "target", formTarget$jscomp$1);
				target$jscomp$0.push(endOfStartTag);
				null !== formActionName && (target$jscomp$0.push(startHiddenInputChunk), pushStringAttribute(target$jscomp$0, "name", formActionName), target$jscomp$0.push(endOfStartTagSelfClosing), formData$jscomp$1?.forEach(pushAdditionalFormField, target$jscomp$0));
				pushInnerHTML(target$jscomp$0, innerHTML$jscomp$3, children$jscomp$4);
				if ("string" === typeof children$jscomp$4) {
					target$jscomp$0.push(stringToChunk(escapeTextForBrowser(children$jscomp$4)));
					var JSCompiler_inline_result$jscomp$1 = null;
				} else JSCompiler_inline_result$jscomp$1 = children$jscomp$4;
				return JSCompiler_inline_result$jscomp$1;
			case "menuitem":
				target$jscomp$0.push(startChunkForTag("menuitem"));
				for (var propKey$jscomp$6 in props) if (hasOwnProperty.call(props, propKey$jscomp$6)) {
					var propValue$jscomp$6 = props[propKey$jscomp$6];
					if (null != propValue$jscomp$6) switch (propKey$jscomp$6) {
						case "children":
						case "dangerouslySetInnerHTML": throw Error("menuitems cannot have `children` nor `dangerouslySetInnerHTML`.");
						default: pushAttribute(target$jscomp$0, propKey$jscomp$6, propValue$jscomp$6);
					}
				}
				target$jscomp$0.push(endOfStartTag);
				return null;
			case "object":
				target$jscomp$0.push(startChunkForTag("object"));
				var children$jscomp$5 = null, innerHTML$jscomp$4 = null, propKey$jscomp$7;
				for (propKey$jscomp$7 in props) if (hasOwnProperty.call(props, propKey$jscomp$7)) {
					var propValue$jscomp$7 = props[propKey$jscomp$7];
					if (null != propValue$jscomp$7) switch (propKey$jscomp$7) {
						case "children":
							children$jscomp$5 = propValue$jscomp$7;
							break;
						case "dangerouslySetInnerHTML":
							innerHTML$jscomp$4 = propValue$jscomp$7;
							break;
						case "data":
							var sanitizedValue = sanitizeURL("" + propValue$jscomp$7);
							if ("" === sanitizedValue) break;
							target$jscomp$0.push(attributeSeparator, stringToChunk("data"), attributeAssign, stringToChunk(escapeTextForBrowser(sanitizedValue)), attributeEnd);
							break;
						default: pushAttribute(target$jscomp$0, propKey$jscomp$7, propValue$jscomp$7);
					}
				}
				target$jscomp$0.push(endOfStartTag);
				pushInnerHTML(target$jscomp$0, innerHTML$jscomp$4, children$jscomp$5);
				if ("string" === typeof children$jscomp$5) {
					target$jscomp$0.push(stringToChunk(escapeTextForBrowser(children$jscomp$5)));
					var JSCompiler_inline_result$jscomp$2 = null;
				} else JSCompiler_inline_result$jscomp$2 = children$jscomp$5;
				return JSCompiler_inline_result$jscomp$2;
			case "title":
				var noscriptTagInScope = formatContext.tagScope & 1, isFallback = formatContext.tagScope & 4;
				if (4 === formatContext.insertionMode || noscriptTagInScope || null != props.itemProp) var JSCompiler_inline_result$jscomp$3 = pushTitleImpl(target$jscomp$0, props);
				else isFallback ? JSCompiler_inline_result$jscomp$3 = null : (pushTitleImpl(renderState.hoistableChunks, props), JSCompiler_inline_result$jscomp$3 = void 0);
				return JSCompiler_inline_result$jscomp$3;
			case "link":
				var noscriptTagInScope$jscomp$0 = formatContext.tagScope & 1, isFallback$jscomp$0 = formatContext.tagScope & 4, rel = props.rel, href = props.href, precedence = props.precedence;
				if (4 === formatContext.insertionMode || noscriptTagInScope$jscomp$0 || null != props.itemProp || "string" !== typeof rel || "string" !== typeof href || "" === href) {
					pushLinkImpl(target$jscomp$0, props);
					var JSCompiler_inline_result$jscomp$4 = null;
				} else if ("stylesheet" === props.rel) if ("string" !== typeof precedence || null != props.disabled || props.onLoad || props.onError) JSCompiler_inline_result$jscomp$4 = pushLinkImpl(target$jscomp$0, props);
				else {
					var styleQueue = renderState.styles.get(precedence), resourceState = resumableState.styleResources.hasOwnProperty(href) ? resumableState.styleResources[href] : void 0;
					if (null !== resourceState) {
						resumableState.styleResources[href] = null;
						styleQueue || (styleQueue = {
							precedence: stringToChunk(escapeTextForBrowser(precedence)),
							rules: [],
							hrefs: [],
							sheets: /* @__PURE__ */ new Map()
						}, renderState.styles.set(precedence, styleQueue));
						var resource = {
							state: 0,
							props: assign({}, props, {
								"data-precedence": props.precedence,
								precedence: null
							})
						};
						if (resourceState) {
							2 === resourceState.length && adoptPreloadCredentials(resource.props, resourceState);
							var preloadResource = renderState.preloads.stylesheets.get(href);
							preloadResource && 0 < preloadResource.length ? preloadResource.length = 0 : resource.state = 1;
						}
						styleQueue.sheets.set(href, resource);
						hoistableState && hoistableState.stylesheets.add(resource);
					} else if (styleQueue) {
						var resource$9 = styleQueue.sheets.get(href);
						resource$9 && hoistableState && hoistableState.stylesheets.add(resource$9);
					}
					textEmbedded && target$jscomp$0.push(textSeparator);
					JSCompiler_inline_result$jscomp$4 = null;
				}
				else props.onLoad || props.onError ? JSCompiler_inline_result$jscomp$4 = pushLinkImpl(target$jscomp$0, props) : (textEmbedded && target$jscomp$0.push(textSeparator), JSCompiler_inline_result$jscomp$4 = isFallback$jscomp$0 ? null : pushLinkImpl(renderState.hoistableChunks, props));
				return JSCompiler_inline_result$jscomp$4;
			case "script":
				var noscriptTagInScope$jscomp$1 = formatContext.tagScope & 1, asyncProp = props.async;
				if ("string" !== typeof props.src || !props.src || !asyncProp || "function" === typeof asyncProp || "symbol" === typeof asyncProp || props.onLoad || props.onError || 4 === formatContext.insertionMode || noscriptTagInScope$jscomp$1 || null != props.itemProp) var JSCompiler_inline_result$jscomp$5 = pushScriptImpl(target$jscomp$0, props);
				else {
					var key = props.src;
					if ("module" === props.type) {
						var resources = resumableState.moduleScriptResources;
						var preloads = renderState.preloads.moduleScripts;
					} else resources = resumableState.scriptResources, preloads = renderState.preloads.scripts;
					var resourceState$jscomp$0 = resources.hasOwnProperty(key) ? resources[key] : void 0;
					if (null !== resourceState$jscomp$0) {
						resources[key] = null;
						var scriptProps = props;
						if (resourceState$jscomp$0) {
							2 === resourceState$jscomp$0.length && (scriptProps = assign({}, props), adoptPreloadCredentials(scriptProps, resourceState$jscomp$0));
							var preloadResource$jscomp$0 = preloads.get(key);
							preloadResource$jscomp$0 && (preloadResource$jscomp$0.length = 0);
						}
						var resource$jscomp$0 = [];
						renderState.scripts.add(resource$jscomp$0);
						pushScriptImpl(resource$jscomp$0, scriptProps);
					}
					textEmbedded && target$jscomp$0.push(textSeparator);
					JSCompiler_inline_result$jscomp$5 = null;
				}
				return JSCompiler_inline_result$jscomp$5;
			case "style":
				var noscriptTagInScope$jscomp$2 = formatContext.tagScope & 1, precedence$jscomp$0 = props.precedence, href$jscomp$0 = props.href, nonce = props.nonce;
				if (4 === formatContext.insertionMode || noscriptTagInScope$jscomp$2 || null != props.itemProp || "string" !== typeof precedence$jscomp$0 || "string" !== typeof href$jscomp$0 || "" === href$jscomp$0) {
					target$jscomp$0.push(startChunkForTag("style"));
					var children$jscomp$6 = null, innerHTML$jscomp$5 = null, propKey$jscomp$8;
					for (propKey$jscomp$8 in props) if (hasOwnProperty.call(props, propKey$jscomp$8)) {
						var propValue$jscomp$8 = props[propKey$jscomp$8];
						if (null != propValue$jscomp$8) switch (propKey$jscomp$8) {
							case "children":
								children$jscomp$6 = propValue$jscomp$8;
								break;
							case "dangerouslySetInnerHTML":
								innerHTML$jscomp$5 = propValue$jscomp$8;
								break;
							default: pushAttribute(target$jscomp$0, propKey$jscomp$8, propValue$jscomp$8);
						}
					}
					target$jscomp$0.push(endOfStartTag);
					var child = Array.isArray(children$jscomp$6) ? 2 > children$jscomp$6.length ? children$jscomp$6[0] : null : children$jscomp$6;
					"function" !== typeof child && "symbol" !== typeof child && null !== child && void 0 !== child && target$jscomp$0.push(stringToChunk(("" + child).replace(styleRegex, styleReplacer)));
					pushInnerHTML(target$jscomp$0, innerHTML$jscomp$5, children$jscomp$6);
					target$jscomp$0.push(endChunkForTag("style"));
					var JSCompiler_inline_result$jscomp$6 = null;
				} else {
					var styleQueue$jscomp$0 = renderState.styles.get(precedence$jscomp$0);
					if (null !== (resumableState.styleResources.hasOwnProperty(href$jscomp$0) ? resumableState.styleResources[href$jscomp$0] : void 0)) {
						resumableState.styleResources[href$jscomp$0] = null;
						styleQueue$jscomp$0 || (styleQueue$jscomp$0 = {
							precedence: stringToChunk(escapeTextForBrowser(precedence$jscomp$0)),
							rules: [],
							hrefs: [],
							sheets: /* @__PURE__ */ new Map()
						}, renderState.styles.set(precedence$jscomp$0, styleQueue$jscomp$0));
						var nonceStyle = renderState.nonce.style;
						if (!nonceStyle || nonceStyle === nonce) {
							styleQueue$jscomp$0.hrefs.push(stringToChunk(escapeTextForBrowser(href$jscomp$0)));
							var target = styleQueue$jscomp$0.rules, children$jscomp$7 = null, innerHTML$jscomp$6 = null, propKey$jscomp$9;
							for (propKey$jscomp$9 in props) if (hasOwnProperty.call(props, propKey$jscomp$9)) {
								var propValue$jscomp$9 = props[propKey$jscomp$9];
								if (null != propValue$jscomp$9) switch (propKey$jscomp$9) {
									case "children":
										children$jscomp$7 = propValue$jscomp$9;
										break;
									case "dangerouslySetInnerHTML": innerHTML$jscomp$6 = propValue$jscomp$9;
								}
							}
							var child$jscomp$0 = Array.isArray(children$jscomp$7) ? 2 > children$jscomp$7.length ? children$jscomp$7[0] : null : children$jscomp$7;
							"function" !== typeof child$jscomp$0 && "symbol" !== typeof child$jscomp$0 && null !== child$jscomp$0 && void 0 !== child$jscomp$0 && target.push(stringToChunk(("" + child$jscomp$0).replace(styleRegex, styleReplacer)));
							pushInnerHTML(target, innerHTML$jscomp$6, children$jscomp$7);
						}
					}
					styleQueue$jscomp$0 && hoistableState && hoistableState.styles.add(styleQueue$jscomp$0);
					textEmbedded && target$jscomp$0.push(textSeparator);
					JSCompiler_inline_result$jscomp$6 = void 0;
				}
				return JSCompiler_inline_result$jscomp$6;
			case "meta":
				var noscriptTagInScope$jscomp$3 = formatContext.tagScope & 1, isFallback$jscomp$1 = formatContext.tagScope & 4;
				if (4 === formatContext.insertionMode || noscriptTagInScope$jscomp$3 || null != props.itemProp) var JSCompiler_inline_result$jscomp$7 = pushSelfClosing(target$jscomp$0, props, "meta");
				else textEmbedded && target$jscomp$0.push(textSeparator), JSCompiler_inline_result$jscomp$7 = isFallback$jscomp$1 ? null : "string" === typeof props.charSet ? pushSelfClosing(renderState.charsetChunks, props, "meta") : "viewport" === props.name ? pushSelfClosing(renderState.viewportChunks, props, "meta") : pushSelfClosing(renderState.hoistableChunks, props, "meta");
				return JSCompiler_inline_result$jscomp$7;
			case "listing":
			case "pre":
				target$jscomp$0.push(startChunkForTag(type));
				var children$jscomp$8 = null, innerHTML$jscomp$7 = null, propKey$jscomp$10;
				for (propKey$jscomp$10 in props) if (hasOwnProperty.call(props, propKey$jscomp$10)) {
					var propValue$jscomp$10 = props[propKey$jscomp$10];
					if (null != propValue$jscomp$10) switch (propKey$jscomp$10) {
						case "children":
							children$jscomp$8 = propValue$jscomp$10;
							break;
						case "dangerouslySetInnerHTML":
							innerHTML$jscomp$7 = propValue$jscomp$10;
							break;
						default: pushAttribute(target$jscomp$0, propKey$jscomp$10, propValue$jscomp$10);
					}
				}
				target$jscomp$0.push(endOfStartTag);
				if (null != innerHTML$jscomp$7) {
					if (null != children$jscomp$8) throw Error("Can only set one of `children` or `props.dangerouslySetInnerHTML`.");
					if ("object" !== typeof innerHTML$jscomp$7 || !("__html" in innerHTML$jscomp$7)) throw Error("`props.dangerouslySetInnerHTML` must be in the form `{__html: ...}`. Please visit https://react.dev/link/dangerously-set-inner-html for more information.");
					var html = innerHTML$jscomp$7.__html;
					null !== html && void 0 !== html && ("string" === typeof html && 0 < html.length && "\n" === html[0] ? target$jscomp$0.push(leadingNewline, stringToChunk(html)) : target$jscomp$0.push(stringToChunk("" + html)));
				}
				"string" === typeof children$jscomp$8 && "\n" === children$jscomp$8[0] && target$jscomp$0.push(leadingNewline);
				return children$jscomp$8;
			case "img":
				var pictureOrNoScriptTagInScope = formatContext.tagScope & 3, src = props.src, srcSet = props.srcSet;
				if (!("lazy" === props.loading || !src && !srcSet || "string" !== typeof src && null != src || "string" !== typeof srcSet && null != srcSet || "low" === props.fetchPriority || pictureOrNoScriptTagInScope) && ("string" !== typeof src || ":" !== src[4] || "d" !== src[0] && "D" !== src[0] || "a" !== src[1] && "A" !== src[1] || "t" !== src[2] && "T" !== src[2] || "a" !== src[3] && "A" !== src[3]) && ("string" !== typeof srcSet || ":" !== srcSet[4] || "d" !== srcSet[0] && "D" !== srcSet[0] || "a" !== srcSet[1] && "A" !== srcSet[1] || "t" !== srcSet[2] && "T" !== srcSet[2] || "a" !== srcSet[3] && "A" !== srcSet[3])) {
					null !== hoistableState && formatContext.tagScope & 64 && (hoistableState.suspenseyImages = !0);
					var sizes = "string" === typeof props.sizes ? props.sizes : void 0, key$jscomp$0 = srcSet ? srcSet + "\n" + (sizes || "") : src, promotablePreloads = renderState.preloads.images, resource$jscomp$1 = promotablePreloads.get(key$jscomp$0);
					if (resource$jscomp$1) {
						if ("high" === props.fetchPriority || 10 > renderState.highImagePreloads.size) promotablePreloads.delete(key$jscomp$0), renderState.highImagePreloads.add(resource$jscomp$1);
					} else if (!resumableState.imageResources.hasOwnProperty(key$jscomp$0)) {
						resumableState.imageResources[key$jscomp$0] = PRELOAD_NO_CREDS;
						var input = props.crossOrigin;
						var JSCompiler_inline_result$jscomp$8 = "string" === typeof input ? "use-credentials" === input ? input : "" : void 0;
						var headers = renderState.headers, header;
						headers && 0 < headers.remainingCapacity && "string" !== typeof props.srcSet && ("high" === props.fetchPriority || 500 > headers.highImagePreloads.length) && (header = getPreloadAsHeader(src, "image", {
							imageSrcSet: props.srcSet,
							imageSizes: props.sizes,
							crossOrigin: JSCompiler_inline_result$jscomp$8,
							integrity: props.integrity,
							nonce: props.nonce,
							type: props.type,
							fetchPriority: props.fetchPriority,
							referrerPolicy: props.refererPolicy
						}), 0 <= (headers.remainingCapacity -= header.length + 2)) ? (renderState.resets.image[key$jscomp$0] = PRELOAD_NO_CREDS, headers.highImagePreloads && (headers.highImagePreloads += ", "), headers.highImagePreloads += header) : (resource$jscomp$1 = [], pushLinkImpl(resource$jscomp$1, {
							rel: "preload",
							as: "image",
							href: srcSet ? void 0 : src,
							imageSrcSet: srcSet,
							imageSizes: sizes,
							crossOrigin: JSCompiler_inline_result$jscomp$8,
							integrity: props.integrity,
							type: props.type,
							fetchPriority: props.fetchPriority,
							referrerPolicy: props.referrerPolicy
						}), "high" === props.fetchPriority || 10 > renderState.highImagePreloads.size ? renderState.highImagePreloads.add(resource$jscomp$1) : (renderState.bulkPreloads.add(resource$jscomp$1), promotablePreloads.set(key$jscomp$0, resource$jscomp$1)));
					}
				}
				return pushSelfClosing(target$jscomp$0, props, "img");
			case "base":
			case "area":
			case "br":
			case "col":
			case "embed":
			case "hr":
			case "keygen":
			case "param":
			case "source":
			case "track":
			case "wbr": return pushSelfClosing(target$jscomp$0, props, type);
			case "annotation-xml":
			case "color-profile":
			case "font-face":
			case "font-face-src":
			case "font-face-uri":
			case "font-face-format":
			case "font-face-name":
			case "missing-glyph": break;
			case "head":
				if (2 > formatContext.insertionMode) {
					var preamble = preambleState || renderState.preamble;
					if (preamble.headChunks) throw Error("The `<head>` tag may only be rendered once.");
					null !== preambleState && target$jscomp$0.push(headPreambleContributionChunk);
					preamble.headChunks = [];
					var JSCompiler_inline_result$jscomp$9 = pushStartSingletonElement(preamble.headChunks, props, "head");
				} else JSCompiler_inline_result$jscomp$9 = pushStartGenericElement(target$jscomp$0, props, "head");
				return JSCompiler_inline_result$jscomp$9;
			case "body":
				if (2 > formatContext.insertionMode) {
					var preamble$jscomp$0 = preambleState || renderState.preamble;
					if (preamble$jscomp$0.bodyChunks) throw Error("The `<body>` tag may only be rendered once.");
					null !== preambleState && target$jscomp$0.push(bodyPreambleContributionChunk);
					preamble$jscomp$0.bodyChunks = [];
					var JSCompiler_inline_result$jscomp$10 = pushStartSingletonElement(preamble$jscomp$0.bodyChunks, props, "body");
				} else JSCompiler_inline_result$jscomp$10 = pushStartGenericElement(target$jscomp$0, props, "body");
				return JSCompiler_inline_result$jscomp$10;
			case "html":
				if (0 === formatContext.insertionMode) {
					var preamble$jscomp$1 = preambleState || renderState.preamble;
					if (preamble$jscomp$1.htmlChunks) throw Error("The `<html>` tag may only be rendered once.");
					null !== preambleState && target$jscomp$0.push(htmlPreambleContributionChunk);
					preamble$jscomp$1.htmlChunks = [doctypeChunk];
					var JSCompiler_inline_result$jscomp$11 = pushStartSingletonElement(preamble$jscomp$1.htmlChunks, props, "html");
				} else JSCompiler_inline_result$jscomp$11 = pushStartGenericElement(target$jscomp$0, props, "html");
				return JSCompiler_inline_result$jscomp$11;
			default: if (-1 !== type.indexOf("-")) {
				target$jscomp$0.push(startChunkForTag(type));
				var children$jscomp$9 = null, innerHTML$jscomp$8 = null, propKey$jscomp$11;
				for (propKey$jscomp$11 in props) if (hasOwnProperty.call(props, propKey$jscomp$11)) {
					var propValue$jscomp$11 = props[propKey$jscomp$11];
					if (null != propValue$jscomp$11) {
						var attributeName = propKey$jscomp$11;
						switch (propKey$jscomp$11) {
							case "children":
								children$jscomp$9 = propValue$jscomp$11;
								break;
							case "dangerouslySetInnerHTML":
								innerHTML$jscomp$8 = propValue$jscomp$11;
								break;
							case "style":
								pushStyleAttribute(target$jscomp$0, propValue$jscomp$11);
								break;
							case "suppressContentEditableWarning":
							case "suppressHydrationWarning":
							case "ref": break;
							case "className": attributeName = "class";
							default: if (isAttributeNameSafe(propKey$jscomp$11) && "function" !== typeof propValue$jscomp$11 && "symbol" !== typeof propValue$jscomp$11 && !1 !== propValue$jscomp$11) {
								if (!0 === propValue$jscomp$11) propValue$jscomp$11 = "";
								else if ("object" === typeof propValue$jscomp$11) continue;
								target$jscomp$0.push(attributeSeparator, stringToChunk(attributeName), attributeAssign, stringToChunk(escapeTextForBrowser(propValue$jscomp$11)), attributeEnd);
							}
						}
					}
				}
				target$jscomp$0.push(endOfStartTag);
				pushInnerHTML(target$jscomp$0, innerHTML$jscomp$8, children$jscomp$9);
				return children$jscomp$9;
			}
		}
		return pushStartGenericElement(target$jscomp$0, props, type);
	}
	var endTagCache = /* @__PURE__ */ new Map();
	function endChunkForTag(tag) {
		var chunk = endTagCache.get(tag);
		void 0 === chunk && (chunk = stringToPrecomputedChunk("</" + tag + ">"), endTagCache.set(tag, chunk));
		return chunk;
	}
	function hoistPreambleState(renderState, preambleState) {
		renderState = renderState.preamble;
		null === renderState.htmlChunks && preambleState.htmlChunks && (renderState.htmlChunks = preambleState.htmlChunks);
		null === renderState.headChunks && preambleState.headChunks && (renderState.headChunks = preambleState.headChunks);
		null === renderState.bodyChunks && preambleState.bodyChunks && (renderState.bodyChunks = preambleState.bodyChunks);
	}
	function writeBootstrap(destination, renderState) {
		renderState = renderState.bootstrapChunks;
		for (var i = 0; i < renderState.length - 1; i++) writeChunk(destination, renderState[i]);
		return i < renderState.length ? (i = renderState[i], renderState.length = 0, writeChunkAndReturn(destination, i)) : !0;
	}
	var shellTimeRuntimeScript = stringToPrecomputedChunk("requestAnimationFrame(function(){$RT=performance.now()});");
	var placeholder1 = stringToPrecomputedChunk("<template id=\"");
	var placeholder2 = stringToPrecomputedChunk("\"></template>");
	var startActivityBoundary = stringToPrecomputedChunk("<!--&-->");
	var endActivityBoundary = stringToPrecomputedChunk("<!--/&-->");
	var startCompletedSuspenseBoundary = stringToPrecomputedChunk("<!--$-->");
	var startPendingSuspenseBoundary1 = stringToPrecomputedChunk("<!--$?--><template id=\"");
	var startPendingSuspenseBoundary2 = stringToPrecomputedChunk("\"></template>");
	var startClientRenderedSuspenseBoundary = stringToPrecomputedChunk("<!--$!-->");
	var endSuspenseBoundary = stringToPrecomputedChunk("<!--/$-->");
	var clientRenderedSuspenseBoundaryError1 = stringToPrecomputedChunk("<template");
	var clientRenderedSuspenseBoundaryErrorAttrInterstitial = stringToPrecomputedChunk("\"");
	var clientRenderedSuspenseBoundaryError1A = stringToPrecomputedChunk(" data-dgst=\"");
	stringToPrecomputedChunk(" data-msg=\"");
	stringToPrecomputedChunk(" data-stck=\"");
	stringToPrecomputedChunk(" data-cstck=\"");
	var clientRenderedSuspenseBoundaryError2 = stringToPrecomputedChunk("></template>");
	function writeStartPendingSuspenseBoundary(destination, renderState, id) {
		writeChunk(destination, startPendingSuspenseBoundary1);
		if (null === id) throw Error("An ID must have been assigned before we can complete the boundary.");
		writeChunk(destination, renderState.boundaryPrefix);
		writeChunk(destination, stringToChunk(id.toString(16)));
		return writeChunkAndReturn(destination, startPendingSuspenseBoundary2);
	}
	var startSegmentHTML = stringToPrecomputedChunk("<div hidden id=\"");
	var startSegmentHTML2 = stringToPrecomputedChunk("\">");
	var endSegmentHTML = stringToPrecomputedChunk("</div>");
	var startSegmentSVG = stringToPrecomputedChunk("<svg aria-hidden=\"true\" style=\"display:none\" id=\"");
	var startSegmentSVG2 = stringToPrecomputedChunk("\">");
	var endSegmentSVG = stringToPrecomputedChunk("</svg>");
	var startSegmentMathML = stringToPrecomputedChunk("<math aria-hidden=\"true\" style=\"display:none\" id=\"");
	var startSegmentMathML2 = stringToPrecomputedChunk("\">");
	var endSegmentMathML = stringToPrecomputedChunk("</math>");
	var startSegmentTable = stringToPrecomputedChunk("<table hidden id=\"");
	var startSegmentTable2 = stringToPrecomputedChunk("\">");
	var endSegmentTable = stringToPrecomputedChunk("</table>");
	var startSegmentTableBody = stringToPrecomputedChunk("<table hidden><tbody id=\"");
	var startSegmentTableBody2 = stringToPrecomputedChunk("\">");
	var endSegmentTableBody = stringToPrecomputedChunk("</tbody></table>");
	var startSegmentTableRow = stringToPrecomputedChunk("<table hidden><tr id=\"");
	var startSegmentTableRow2 = stringToPrecomputedChunk("\">");
	var endSegmentTableRow = stringToPrecomputedChunk("</tr></table>");
	var startSegmentColGroup = stringToPrecomputedChunk("<table hidden><colgroup id=\"");
	var startSegmentColGroup2 = stringToPrecomputedChunk("\">");
	var endSegmentColGroup = stringToPrecomputedChunk("</colgroup></table>");
	function writeStartSegment(destination, renderState, formatContext, id) {
		switch (formatContext.insertionMode) {
			case 0:
			case 1:
			case 3:
			case 2: return writeChunk(destination, startSegmentHTML), writeChunk(destination, renderState.segmentPrefix), writeChunk(destination, stringToChunk(id.toString(16))), writeChunkAndReturn(destination, startSegmentHTML2);
			case 4: return writeChunk(destination, startSegmentSVG), writeChunk(destination, renderState.segmentPrefix), writeChunk(destination, stringToChunk(id.toString(16))), writeChunkAndReturn(destination, startSegmentSVG2);
			case 5: return writeChunk(destination, startSegmentMathML), writeChunk(destination, renderState.segmentPrefix), writeChunk(destination, stringToChunk(id.toString(16))), writeChunkAndReturn(destination, startSegmentMathML2);
			case 6: return writeChunk(destination, startSegmentTable), writeChunk(destination, renderState.segmentPrefix), writeChunk(destination, stringToChunk(id.toString(16))), writeChunkAndReturn(destination, startSegmentTable2);
			case 7: return writeChunk(destination, startSegmentTableBody), writeChunk(destination, renderState.segmentPrefix), writeChunk(destination, stringToChunk(id.toString(16))), writeChunkAndReturn(destination, startSegmentTableBody2);
			case 8: return writeChunk(destination, startSegmentTableRow), writeChunk(destination, renderState.segmentPrefix), writeChunk(destination, stringToChunk(id.toString(16))), writeChunkAndReturn(destination, startSegmentTableRow2);
			case 9: return writeChunk(destination, startSegmentColGroup), writeChunk(destination, renderState.segmentPrefix), writeChunk(destination, stringToChunk(id.toString(16))), writeChunkAndReturn(destination, startSegmentColGroup2);
			default: throw Error("Unknown insertion mode. This is a bug in React.");
		}
	}
	function writeEndSegment(destination, formatContext) {
		switch (formatContext.insertionMode) {
			case 0:
			case 1:
			case 3:
			case 2: return writeChunkAndReturn(destination, endSegmentHTML);
			case 4: return writeChunkAndReturn(destination, endSegmentSVG);
			case 5: return writeChunkAndReturn(destination, endSegmentMathML);
			case 6: return writeChunkAndReturn(destination, endSegmentTable);
			case 7: return writeChunkAndReturn(destination, endSegmentTableBody);
			case 8: return writeChunkAndReturn(destination, endSegmentTableRow);
			case 9: return writeChunkAndReturn(destination, endSegmentColGroup);
			default: throw Error("Unknown insertion mode. This is a bug in React.");
		}
	}
	var completeSegmentScript1Full = stringToPrecomputedChunk("$RS=function(a,b){a=document.getElementById(a);b=document.getElementById(b);for(a.parentNode.removeChild(a);a.firstChild;)b.parentNode.insertBefore(a.firstChild,b);b.parentNode.removeChild(b)};$RS(\"");
	var completeSegmentScript1Partial = stringToPrecomputedChunk("$RS(\"");
	var completeSegmentScript2 = stringToPrecomputedChunk("\",\"");
	var completeSegmentScriptEnd = stringToPrecomputedChunk("\")<\/script>");
	stringToPrecomputedChunk("<template data-rsi=\"\" data-sid=\"");
	stringToPrecomputedChunk("\" data-pid=\"");
	var completeBoundaryScriptFunctionOnly = stringToPrecomputedChunk("$RB=[];$RV=function(a){$RT=performance.now();for(var b=0;b<a.length;b+=2){var c=a[b],e=a[b+1];null!==e.parentNode&&e.parentNode.removeChild(e);var f=c.parentNode;if(f){var g=c.previousSibling,h=0;do{if(c&&8===c.nodeType){var d=c.data;if(\"/$\"===d||\"/&\"===d)if(0===h)break;else h--;else\"$\"!==d&&\"$?\"!==d&&\"$~\"!==d&&\"$!\"!==d&&\"&\"!==d||h++}d=c.nextSibling;f.removeChild(c);c=d}while(c);for(;e.firstChild;)f.insertBefore(e.firstChild,c);g.data=\"$\";g._reactRetry&&requestAnimationFrame(g._reactRetry)}}a.length=0};\n$RC=function(a,b){if(b=document.getElementById(b))(a=document.getElementById(a))?(a.previousSibling.data=\"$~\",$RB.push(a,b),2===$RB.length&&(\"number\"!==typeof $RT?requestAnimationFrame($RV.bind(null,$RB)):(a=performance.now(),setTimeout($RV.bind(null,$RB),2300>a&&2E3<a?2300-a:$RT+300-a)))):b.parentNode.removeChild(b)};");
	stringToChunk("$RV=function(A,g){function k(a,b){var e=a.getAttribute(b);e&&(b=a.style,l.push(a,b.viewTransitionName,b.viewTransitionClass),\"auto\"!==e&&(b.viewTransitionClass=e),(a=a.getAttribute(\"vt-name\"))||(a=\"_T_\"+K++ +\"_\"),b.viewTransitionName=a,B=!0)}var B=!1,K=0,l=[];try{var f=document.__reactViewTransition;if(f){f.finished.finally($RV.bind(null,g));return}var m=new Map;for(f=1;f<g.length;f+=2)for(var h=g[f].querySelectorAll(\"[vt-share]\"),d=0;d<h.length;d++){var c=h[d];m.set(c.getAttribute(\"vt-name\"),c)}var u=[];for(h=0;h<g.length;h+=2){var C=g[h],x=C.parentNode;if(x){var v=x.getBoundingClientRect();if(v.left||v.top||v.width||v.height){c=C;for(f=0;c;){if(8===c.nodeType){var r=c.data;if(\"/$\"===r)if(0===f)break;else f--;else\"$\"!==r&&\"$?\"!==r&&\"$~\"!==r&&\"$!\"!==r||f++}else if(1===c.nodeType){d=c;var D=d.getAttribute(\"vt-name\"),y=m.get(D);k(d,y?\"vt-share\":\"vt-exit\");y&&(k(y,\"vt-share\"),m.set(D,null));var E=d.querySelectorAll(\"[vt-share]\");for(d=0;d<E.length;d++){var F=E[d],G=F.getAttribute(\"vt-name\"),\nH=m.get(G);H&&(k(F,\"vt-share\"),k(H,\"vt-share\"),m.set(G,null))}}c=c.nextSibling}for(var I=g[h+1],t=I.firstElementChild;t;)null!==m.get(t.getAttribute(\"vt-name\"))&&k(t,\"vt-enter\"),t=t.nextElementSibling;c=x;do for(var n=c.firstElementChild;n;){var J=n.getAttribute(\"vt-update\");J&&\"none\"!==J&&!l.includes(n)&&k(n,\"vt-update\");n=n.nextElementSibling}while((c=c.parentNode)&&1===c.nodeType&&\"none\"!==c.getAttribute(\"vt-update\"));u.push.apply(u,I.querySelectorAll('img[src]:not([loading=\"lazy\"])'))}}}if(B){var z=\ndocument.__reactViewTransition=document.startViewTransition({update:function(){A(g);for(var a=[document.documentElement.clientHeight,document.fonts.ready],b={},e=0;e<u.length;b={g:b.g},e++)if(b.g=u[e],!b.g.complete){var p=b.g.getBoundingClientRect();0<p.bottom&&0<p.right&&p.top<window.innerHeight&&p.left<window.innerWidth&&(p=new Promise(function(w){return function(q){w.g.addEventListener(\"load\",q);w.g.addEventListener(\"error\",q)}}(b)),a.push(p))}return Promise.race([Promise.all(a),new Promise(function(w){var q=\nperformance.now();setTimeout(w,2300>q&&2E3<q?2300-q:500)})])},types:[]});z.ready.finally(function(){for(var a=l.length-3;0<=a;a-=3){var b=l[a],e=b.style;e.viewTransitionName=l[a+1];e.viewTransitionClass=l[a+1];\"\"===b.getAttribute(\"style\")&&b.removeAttribute(\"style\")}});z.finished.finally(function(){document.__reactViewTransition===z&&(document.__reactViewTransition=null)});$RB=[];return}}catch(a){}A(g)}.bind(null,$RV);");
	var completeBoundaryScript1Partial = stringToPrecomputedChunk("$RC(\"");
	var completeBoundaryWithStylesScript1FullPartial = stringToPrecomputedChunk("$RM=new Map;$RR=function(n,w,p){function u(q){this._p=null;q()}for(var r=new Map,t=document,h,b,e=t.querySelectorAll(\"link[data-precedence],style[data-precedence]\"),v=[],k=0;b=e[k++];)\"not all\"===b.getAttribute(\"media\")?v.push(b):(\"LINK\"===b.tagName&&$RM.set(b.getAttribute(\"href\"),b),r.set(b.dataset.precedence,h=b));e=0;b=[];var l,a;for(k=!0;;){if(k){var f=p[e++];if(!f){k=!1;e=0;continue}var c=!1,m=0;var d=f[m++];if(a=$RM.get(d)){var g=a._p;c=!0}else{a=t.createElement(\"link\");a.href=d;a.rel=\n\"stylesheet\";for(a.dataset.precedence=l=f[m++];g=f[m++];)a.setAttribute(g,f[m++]);g=a._p=new Promise(function(q,x){a.onload=u.bind(a,q);a.onerror=u.bind(a,x)});$RM.set(d,a)}d=a.getAttribute(\"media\");!g||d&&!matchMedia(d).matches||b.push(g);if(c)continue}else{a=v[e++];if(!a)break;l=a.getAttribute(\"data-precedence\");a.removeAttribute(\"media\")}c=r.get(l)||h;c===h&&(h=a);r.set(l,a);c?c.parentNode.insertBefore(a,c.nextSibling):(c=t.head,c.insertBefore(a,c.firstChild))}if(p=document.getElementById(n))p.previousSibling.data=\n\"$~\";Promise.all(b).then($RC.bind(null,n,w),$RX.bind(null,n,\"CSS failed to load\"))};$RR(\"");
	var completeBoundaryWithStylesScript1Partial = stringToPrecomputedChunk("$RR(\"");
	var completeBoundaryScript2 = stringToPrecomputedChunk("\",\"");
	var completeBoundaryScript3a = stringToPrecomputedChunk("\",");
	var completeBoundaryScript3b = stringToPrecomputedChunk("\"");
	var completeBoundaryScriptEnd = stringToPrecomputedChunk(")<\/script>");
	stringToPrecomputedChunk("<template data-rci=\"\" data-bid=\"");
	stringToPrecomputedChunk("<template data-rri=\"\" data-bid=\"");
	stringToPrecomputedChunk("\" data-sid=\"");
	stringToPrecomputedChunk("\" data-sty=\"");
	var clientRenderScriptFunctionOnly = stringToPrecomputedChunk("$RX=function(b,c,d,e,f){var a=document.getElementById(b);a&&(b=a.previousSibling,b.data=\"$!\",a=a.dataset,c&&(a.dgst=c),d&&(a.msg=d),e&&(a.stck=e),f&&(a.cstck=f),b._reactRetry&&b._reactRetry())};");
	var clientRenderScript1Full = stringToPrecomputedChunk("$RX=function(b,c,d,e,f){var a=document.getElementById(b);a&&(b=a.previousSibling,b.data=\"$!\",a=a.dataset,c&&(a.dgst=c),d&&(a.msg=d),e&&(a.stck=e),f&&(a.cstck=f),b._reactRetry&&b._reactRetry())};;$RX(\"");
	var clientRenderScript1Partial = stringToPrecomputedChunk("$RX(\"");
	var clientRenderScript1A = stringToPrecomputedChunk("\"");
	var clientRenderErrorScriptArgInterstitial = stringToPrecomputedChunk(",");
	var clientRenderScriptEnd = stringToPrecomputedChunk(")<\/script>");
	stringToPrecomputedChunk("<template data-rxi=\"\" data-bid=\"");
	stringToPrecomputedChunk("\" data-dgst=\"");
	stringToPrecomputedChunk("\" data-msg=\"");
	stringToPrecomputedChunk("\" data-stck=\"");
	stringToPrecomputedChunk("\" data-cstck=\"");
	var regexForJSStringsInInstructionScripts = /[<\u2028\u2029]/g;
	function escapeJSStringsForInstructionScripts(input) {
		return JSON.stringify(input).replace(regexForJSStringsInInstructionScripts, function(match) {
			switch (match) {
				case "<": return "\\u003c";
				case "\u2028": return "\\u2028";
				case "\u2029": return "\\u2029";
				default: throw Error("escapeJSStringsForInstructionScripts encountered a match it does not know how to replace. this means the match regex and the replacement characters are no longer in sync. This is a bug in React");
			}
		});
	}
	var regexForJSStringsInScripts = /[&><\u2028\u2029]/g;
	function escapeJSObjectForInstructionScripts(input) {
		return JSON.stringify(input).replace(regexForJSStringsInScripts, function(match) {
			switch (match) {
				case "&": return "\\u0026";
				case ">": return "\\u003e";
				case "<": return "\\u003c";
				case "\u2028": return "\\u2028";
				case "\u2029": return "\\u2029";
				default: throw Error("escapeJSObjectForInstructionScripts encountered a match it does not know how to replace. this means the match regex and the replacement characters are no longer in sync. This is a bug in React");
			}
		});
	}
	var lateStyleTagResourceOpen1 = stringToPrecomputedChunk(" media=\"not all\" data-precedence=\"");
	var lateStyleTagResourceOpen2 = stringToPrecomputedChunk("\" data-href=\"");
	var lateStyleTagResourceOpen3 = stringToPrecomputedChunk("\">");
	var lateStyleTagTemplateClose = stringToPrecomputedChunk("</style>");
	var currentlyRenderingBoundaryHasStylesToHoist = !1;
	var destinationHasCapacity = !0;
	function flushStyleTagsLateForBoundary(styleQueue) {
		var rules = styleQueue.rules, hrefs = styleQueue.hrefs, i = 0;
		if (hrefs.length) {
			writeChunk(this, currentlyFlushingRenderState.startInlineStyle);
			writeChunk(this, lateStyleTagResourceOpen1);
			writeChunk(this, styleQueue.precedence);
			for (writeChunk(this, lateStyleTagResourceOpen2); i < hrefs.length - 1; i++) writeChunk(this, hrefs[i]), writeChunk(this, spaceSeparator);
			writeChunk(this, hrefs[i]);
			writeChunk(this, lateStyleTagResourceOpen3);
			for (i = 0; i < rules.length; i++) writeChunk(this, rules[i]);
			destinationHasCapacity = writeChunkAndReturn(this, lateStyleTagTemplateClose);
			currentlyRenderingBoundaryHasStylesToHoist = !0;
			rules.length = 0;
			hrefs.length = 0;
		}
	}
	function hasStylesToHoist(stylesheet) {
		return 2 !== stylesheet.state ? currentlyRenderingBoundaryHasStylesToHoist = !0 : !1;
	}
	function writeHoistablesForBoundary(destination, hoistableState, renderState) {
		currentlyRenderingBoundaryHasStylesToHoist = !1;
		destinationHasCapacity = !0;
		currentlyFlushingRenderState = renderState;
		hoistableState.styles.forEach(flushStyleTagsLateForBoundary, destination);
		currentlyFlushingRenderState = null;
		hoistableState.stylesheets.forEach(hasStylesToHoist);
		currentlyRenderingBoundaryHasStylesToHoist && (renderState.stylesToHoist = !0);
		return destinationHasCapacity;
	}
	function flushResource(resource) {
		for (var i = 0; i < resource.length; i++) writeChunk(this, resource[i]);
		resource.length = 0;
	}
	var stylesheetFlushingQueue = [];
	function flushStyleInPreamble(stylesheet) {
		pushLinkImpl(stylesheetFlushingQueue, stylesheet.props);
		for (var i = 0; i < stylesheetFlushingQueue.length; i++) writeChunk(this, stylesheetFlushingQueue[i]);
		stylesheetFlushingQueue.length = 0;
		stylesheet.state = 2;
	}
	var styleTagResourceOpen1 = stringToPrecomputedChunk(" data-precedence=\"");
	var styleTagResourceOpen2 = stringToPrecomputedChunk("\" data-href=\"");
	var spaceSeparator = stringToPrecomputedChunk(" ");
	var styleTagResourceOpen3 = stringToPrecomputedChunk("\">");
	var styleTagResourceClose = stringToPrecomputedChunk("</style>");
	function flushStylesInPreamble(styleQueue) {
		var hasStylesheets = 0 < styleQueue.sheets.size;
		styleQueue.sheets.forEach(flushStyleInPreamble, this);
		styleQueue.sheets.clear();
		var rules = styleQueue.rules, hrefs = styleQueue.hrefs;
		if (!hasStylesheets || hrefs.length) {
			writeChunk(this, currentlyFlushingRenderState.startInlineStyle);
			writeChunk(this, styleTagResourceOpen1);
			writeChunk(this, styleQueue.precedence);
			styleQueue = 0;
			if (hrefs.length) {
				for (writeChunk(this, styleTagResourceOpen2); styleQueue < hrefs.length - 1; styleQueue++) writeChunk(this, hrefs[styleQueue]), writeChunk(this, spaceSeparator);
				writeChunk(this, hrefs[styleQueue]);
			}
			writeChunk(this, styleTagResourceOpen3);
			for (styleQueue = 0; styleQueue < rules.length; styleQueue++) writeChunk(this, rules[styleQueue]);
			writeChunk(this, styleTagResourceClose);
			rules.length = 0;
			hrefs.length = 0;
		}
	}
	function preloadLateStyle(stylesheet) {
		if (0 === stylesheet.state) {
			stylesheet.state = 1;
			var props = stylesheet.props;
			pushLinkImpl(stylesheetFlushingQueue, {
				rel: "preload",
				as: "style",
				href: stylesheet.props.href,
				crossOrigin: props.crossOrigin,
				fetchPriority: props.fetchPriority,
				integrity: props.integrity,
				media: props.media,
				hrefLang: props.hrefLang,
				referrerPolicy: props.referrerPolicy
			});
			for (stylesheet = 0; stylesheet < stylesheetFlushingQueue.length; stylesheet++) writeChunk(this, stylesheetFlushingQueue[stylesheet]);
			stylesheetFlushingQueue.length = 0;
		}
	}
	function preloadLateStyles(styleQueue) {
		styleQueue.sheets.forEach(preloadLateStyle, this);
		styleQueue.sheets.clear();
	}
	stringToPrecomputedChunk("<link rel=\"expect\" href=\"#");
	stringToPrecomputedChunk("\" blocking=\"render\"/>");
	var completedShellIdAttributeStart = stringToPrecomputedChunk(" id=\"");
	function pushCompletedShellIdAttribute(target, resumableState) {
		0 === (resumableState.instructions & 32) && (resumableState.instructions |= 32, target.push(completedShellIdAttributeStart, stringToChunk(escapeTextForBrowser("_" + resumableState.idPrefix + "R_")), attributeEnd));
	}
	var arrayFirstOpenBracket = stringToPrecomputedChunk("[");
	var arraySubsequentOpenBracket = stringToPrecomputedChunk(",[");
	var arrayInterstitial = stringToPrecomputedChunk(",");
	var arrayCloseBracket = stringToPrecomputedChunk("]");
	function writeStyleResourceDependenciesInJS(destination, hoistableState) {
		writeChunk(destination, arrayFirstOpenBracket);
		var nextArrayOpenBrackChunk = arrayFirstOpenBracket;
		hoistableState.stylesheets.forEach(function(resource) {
			if (2 !== resource.state) if (3 === resource.state) writeChunk(destination, nextArrayOpenBrackChunk), writeChunk(destination, stringToChunk(escapeJSObjectForInstructionScripts("" + resource.props.href))), writeChunk(destination, arrayCloseBracket), nextArrayOpenBrackChunk = arraySubsequentOpenBracket;
			else {
				writeChunk(destination, nextArrayOpenBrackChunk);
				var precedence = resource.props["data-precedence"], props = resource.props;
				writeChunk(destination, stringToChunk(escapeJSObjectForInstructionScripts(sanitizeURL("" + resource.props.href))));
				precedence = "" + precedence;
				writeChunk(destination, arrayInterstitial);
				writeChunk(destination, stringToChunk(escapeJSObjectForInstructionScripts(precedence)));
				for (var propKey in props) if (hasOwnProperty.call(props, propKey) && (precedence = props[propKey], null != precedence)) switch (propKey) {
					case "href":
					case "rel":
					case "precedence":
					case "data-precedence": break;
					case "children":
					case "dangerouslySetInnerHTML": throw Error("link is a self-closing tag and must neither have `children` nor use `dangerouslySetInnerHTML`.");
					default: writeStyleResourceAttributeInJS(destination, propKey, precedence);
				}
				writeChunk(destination, arrayCloseBracket);
				nextArrayOpenBrackChunk = arraySubsequentOpenBracket;
				resource.state = 3;
			}
		});
		writeChunk(destination, arrayCloseBracket);
	}
	function writeStyleResourceAttributeInJS(destination, name, value) {
		var attributeName = name.toLowerCase();
		switch (typeof value) {
			case "function":
			case "symbol": return;
		}
		switch (name) {
			case "innerHTML":
			case "dangerouslySetInnerHTML":
			case "suppressContentEditableWarning":
			case "suppressHydrationWarning":
			case "style":
			case "ref": return;
			case "className":
				attributeName = "class";
				name = "" + value;
				break;
			case "hidden":
				if (!1 === value) return;
				name = "";
				break;
			case "src":
			case "href":
				value = sanitizeURL(value);
				name = "" + value;
				break;
			default:
				if (2 < name.length && ("o" === name[0] || "O" === name[0]) && ("n" === name[1] || "N" === name[1]) || !isAttributeNameSafe(name)) return;
				name = "" + value;
		}
		writeChunk(destination, arrayInterstitial);
		writeChunk(destination, stringToChunk(escapeJSObjectForInstructionScripts(attributeName)));
		writeChunk(destination, arrayInterstitial);
		writeChunk(destination, stringToChunk(escapeJSObjectForInstructionScripts(name)));
	}
	function createHoistableState() {
		return {
			styles: /* @__PURE__ */ new Set(),
			stylesheets: /* @__PURE__ */ new Set(),
			suspenseyImages: !1
		};
	}
	function prefetchDNS(href) {
		var request = resolveRequest();
		if (request) {
			var resumableState = request.resumableState, renderState = request.renderState;
			if ("string" === typeof href && href) {
				if (!resumableState.dnsResources.hasOwnProperty(href)) {
					resumableState.dnsResources[href] = null;
					resumableState = renderState.headers;
					var header, JSCompiler_temp;
					if (JSCompiler_temp = resumableState && 0 < resumableState.remainingCapacity) JSCompiler_temp = (header = "<" + ("" + href).replace(regexForHrefInLinkHeaderURLContext, escapeHrefForLinkHeaderURLContextReplacer) + ">; rel=dns-prefetch", 0 <= (resumableState.remainingCapacity -= header.length + 2));
					JSCompiler_temp ? (renderState.resets.dns[href] = null, resumableState.preconnects && (resumableState.preconnects += ", "), resumableState.preconnects += header) : (header = [], pushLinkImpl(header, {
						href,
						rel: "dns-prefetch"
					}), renderState.preconnects.add(header));
				}
				enqueueFlush(request);
			}
		} else previousDispatcher.D(href);
	}
	function preconnect(href, crossOrigin) {
		var request = resolveRequest();
		if (request) {
			var resumableState = request.resumableState, renderState = request.renderState;
			if ("string" === typeof href && href) {
				var bucket = "use-credentials" === crossOrigin ? "credentials" : "string" === typeof crossOrigin ? "anonymous" : "default";
				if (!resumableState.connectResources[bucket].hasOwnProperty(href)) {
					resumableState.connectResources[bucket][href] = null;
					resumableState = renderState.headers;
					var header, JSCompiler_temp;
					if (JSCompiler_temp = resumableState && 0 < resumableState.remainingCapacity) {
						JSCompiler_temp = "<" + ("" + href).replace(regexForHrefInLinkHeaderURLContext, escapeHrefForLinkHeaderURLContextReplacer) + ">; rel=preconnect";
						if ("string" === typeof crossOrigin) {
							var escapedCrossOrigin = ("" + crossOrigin).replace(regexForLinkHeaderQuotedParamValueContext, escapeStringForLinkHeaderQuotedParamValueContextReplacer);
							JSCompiler_temp += "; crossorigin=\"" + escapedCrossOrigin + "\"";
						}
						JSCompiler_temp = (header = JSCompiler_temp, 0 <= (resumableState.remainingCapacity -= header.length + 2));
					}
					JSCompiler_temp ? (renderState.resets.connect[bucket][href] = null, resumableState.preconnects && (resumableState.preconnects += ", "), resumableState.preconnects += header) : (bucket = [], pushLinkImpl(bucket, {
						rel: "preconnect",
						href,
						crossOrigin
					}), renderState.preconnects.add(bucket));
				}
				enqueueFlush(request);
			}
		} else previousDispatcher.C(href, crossOrigin);
	}
	function preload(href, as, options) {
		var request = resolveRequest();
		if (request) {
			var resumableState = request.resumableState, renderState = request.renderState;
			if (as && href) {
				switch (as) {
					case "image":
						if (options) {
							var imageSrcSet = options.imageSrcSet;
							var imageSizes = options.imageSizes;
							var fetchPriority = options.fetchPriority;
						}
						var key = imageSrcSet ? imageSrcSet + "\n" + (imageSizes || "") : href;
						if (resumableState.imageResources.hasOwnProperty(key)) return;
						resumableState.imageResources[key] = PRELOAD_NO_CREDS;
						resumableState = renderState.headers;
						var header;
						resumableState && 0 < resumableState.remainingCapacity && "string" !== typeof imageSrcSet && "high" === fetchPriority && (header = getPreloadAsHeader(href, as, options), 0 <= (resumableState.remainingCapacity -= header.length + 2)) ? (renderState.resets.image[key] = PRELOAD_NO_CREDS, resumableState.highImagePreloads && (resumableState.highImagePreloads += ", "), resumableState.highImagePreloads += header) : (resumableState = [], pushLinkImpl(resumableState, assign({
							rel: "preload",
							href: imageSrcSet ? void 0 : href,
							as
						}, options)), "high" === fetchPriority ? renderState.highImagePreloads.add(resumableState) : (renderState.bulkPreloads.add(resumableState), renderState.preloads.images.set(key, resumableState)));
						break;
					case "style":
						if (resumableState.styleResources.hasOwnProperty(href)) return;
						imageSrcSet = [];
						pushLinkImpl(imageSrcSet, assign({
							rel: "preload",
							href,
							as
						}, options));
						resumableState.styleResources[href] = !options || "string" !== typeof options.crossOrigin && "string" !== typeof options.integrity ? PRELOAD_NO_CREDS : [options.crossOrigin, options.integrity];
						renderState.preloads.stylesheets.set(href, imageSrcSet);
						renderState.bulkPreloads.add(imageSrcSet);
						break;
					case "script":
						if (resumableState.scriptResources.hasOwnProperty(href)) return;
						imageSrcSet = [];
						renderState.preloads.scripts.set(href, imageSrcSet);
						renderState.bulkPreloads.add(imageSrcSet);
						pushLinkImpl(imageSrcSet, assign({
							rel: "preload",
							href,
							as
						}, options));
						resumableState.scriptResources[href] = !options || "string" !== typeof options.crossOrigin && "string" !== typeof options.integrity ? PRELOAD_NO_CREDS : [options.crossOrigin, options.integrity];
						break;
					default:
						if (resumableState.unknownResources.hasOwnProperty(as)) {
							if (imageSrcSet = resumableState.unknownResources[as], imageSrcSet.hasOwnProperty(href)) return;
						} else imageSrcSet = {}, resumableState.unknownResources[as] = imageSrcSet;
						imageSrcSet[href] = PRELOAD_NO_CREDS;
						if ((resumableState = renderState.headers) && 0 < resumableState.remainingCapacity && "font" === as && (key = getPreloadAsHeader(href, as, options), 0 <= (resumableState.remainingCapacity -= key.length + 2))) renderState.resets.font[href] = PRELOAD_NO_CREDS, resumableState.fontPreloads && (resumableState.fontPreloads += ", "), resumableState.fontPreloads += key;
						else switch (resumableState = [], href = assign({
							rel: "preload",
							href,
							as
						}, options), pushLinkImpl(resumableState, href), as) {
							case "font":
								renderState.fontPreloads.add(resumableState);
								break;
							default: renderState.bulkPreloads.add(resumableState);
						}
				}
				enqueueFlush(request);
			}
		} else previousDispatcher.L(href, as, options);
	}
	function preloadModule(href, options) {
		var request = resolveRequest();
		if (request) {
			var resumableState = request.resumableState, renderState = request.renderState;
			if (href) {
				var as = options && "string" === typeof options.as ? options.as : "script";
				switch (as) {
					case "script":
						if (resumableState.moduleScriptResources.hasOwnProperty(href)) return;
						as = [];
						resumableState.moduleScriptResources[href] = !options || "string" !== typeof options.crossOrigin && "string" !== typeof options.integrity ? PRELOAD_NO_CREDS : [options.crossOrigin, options.integrity];
						renderState.preloads.moduleScripts.set(href, as);
						break;
					default:
						if (resumableState.moduleUnknownResources.hasOwnProperty(as)) {
							var resources = resumableState.unknownResources[as];
							if (resources.hasOwnProperty(href)) return;
						} else resources = {}, resumableState.moduleUnknownResources[as] = resources;
						as = [];
						resources[href] = PRELOAD_NO_CREDS;
				}
				pushLinkImpl(as, assign({
					rel: "modulepreload",
					href
				}, options));
				renderState.bulkPreloads.add(as);
				enqueueFlush(request);
			}
		} else previousDispatcher.m(href, options);
	}
	function preinitStyle(href, precedence, options) {
		var request = resolveRequest();
		if (request) {
			var resumableState = request.resumableState, renderState = request.renderState;
			if (href) {
				precedence = precedence || "default";
				var styleQueue = renderState.styles.get(precedence), resourceState = resumableState.styleResources.hasOwnProperty(href) ? resumableState.styleResources[href] : void 0;
				null !== resourceState && (resumableState.styleResources[href] = null, styleQueue || (styleQueue = {
					precedence: stringToChunk(escapeTextForBrowser(precedence)),
					rules: [],
					hrefs: [],
					sheets: /* @__PURE__ */ new Map()
				}, renderState.styles.set(precedence, styleQueue)), precedence = {
					state: 0,
					props: assign({
						rel: "stylesheet",
						href,
						"data-precedence": precedence
					}, options)
				}, resourceState && (2 === resourceState.length && adoptPreloadCredentials(precedence.props, resourceState), (renderState = renderState.preloads.stylesheets.get(href)) && 0 < renderState.length ? renderState.length = 0 : precedence.state = 1), styleQueue.sheets.set(href, precedence), enqueueFlush(request));
			}
		} else previousDispatcher.S(href, precedence, options);
	}
	function preinitScript(src, options) {
		var request = resolveRequest();
		if (request) {
			var resumableState = request.resumableState, renderState = request.renderState;
			if (src) {
				var resourceState = resumableState.scriptResources.hasOwnProperty(src) ? resumableState.scriptResources[src] : void 0;
				null !== resourceState && (resumableState.scriptResources[src] = null, options = assign({
					src,
					async: !0
				}, options), resourceState && (2 === resourceState.length && adoptPreloadCredentials(options, resourceState), src = renderState.preloads.scripts.get(src)) && (src.length = 0), src = [], renderState.scripts.add(src), pushScriptImpl(src, options), enqueueFlush(request));
			}
		} else previousDispatcher.X(src, options);
	}
	function preinitModuleScript(src, options) {
		var request = resolveRequest();
		if (request) {
			var resumableState = request.resumableState, renderState = request.renderState;
			if (src) {
				var resourceState = resumableState.moduleScriptResources.hasOwnProperty(src) ? resumableState.moduleScriptResources[src] : void 0;
				null !== resourceState && (resumableState.moduleScriptResources[src] = null, options = assign({
					src,
					type: "module",
					async: !0
				}, options), resourceState && (2 === resourceState.length && adoptPreloadCredentials(options, resourceState), src = renderState.preloads.moduleScripts.get(src)) && (src.length = 0), src = [], renderState.scripts.add(src), pushScriptImpl(src, options), enqueueFlush(request));
			}
		} else previousDispatcher.M(src, options);
	}
	function adoptPreloadCredentials(target, preloadState) {
		target.crossOrigin ??= preloadState[0];
		target.integrity ??= preloadState[1];
	}
	function getPreloadAsHeader(href, as, params) {
		href = ("" + href).replace(regexForHrefInLinkHeaderURLContext, escapeHrefForLinkHeaderURLContextReplacer);
		as = ("" + as).replace(regexForLinkHeaderQuotedParamValueContext, escapeStringForLinkHeaderQuotedParamValueContextReplacer);
		as = "<" + href + ">; rel=preload; as=\"" + as + "\"";
		for (var paramName in params) hasOwnProperty.call(params, paramName) && (href = params[paramName], "string" === typeof href && (as += "; " + paramName.toLowerCase() + "=\"" + ("" + href).replace(regexForLinkHeaderQuotedParamValueContext, escapeStringForLinkHeaderQuotedParamValueContextReplacer) + "\""));
		return as;
	}
	var regexForHrefInLinkHeaderURLContext = /[<>\r\n]/g;
	function escapeHrefForLinkHeaderURLContextReplacer(match) {
		switch (match) {
			case "<": return "%3C";
			case ">": return "%3E";
			case "\n": return "%0A";
			case "\r": return "%0D";
			default: throw Error("escapeLinkHrefForHeaderContextReplacer encountered a match it does not know how to replace. this means the match regex and the replacement characters are no longer in sync. This is a bug in React");
		}
	}
	var regexForLinkHeaderQuotedParamValueContext = /["';,\r\n]/g;
	function escapeStringForLinkHeaderQuotedParamValueContextReplacer(match) {
		switch (match) {
			case "\"": return "%22";
			case "'": return "%27";
			case ";": return "%3B";
			case ",": return "%2C";
			case "\n": return "%0A";
			case "\r": return "%0D";
			default: throw Error("escapeStringForLinkHeaderQuotedParamValueContextReplacer encountered a match it does not know how to replace. this means the match regex and the replacement characters are no longer in sync. This is a bug in React");
		}
	}
	function hoistStyleQueueDependency(styleQueue) {
		this.styles.add(styleQueue);
	}
	function hoistStylesheetDependency(stylesheet) {
		this.stylesheets.add(stylesheet);
	}
	function hoistHoistables(parentState, childState) {
		childState.styles.forEach(hoistStyleQueueDependency, parentState);
		childState.stylesheets.forEach(hoistStylesheetDependency, parentState);
		childState.suspenseyImages && (parentState.suspenseyImages = !0);
	}
	function hasSuspenseyContent(hoistableState) {
		return 0 < hoistableState.stylesheets.size || hoistableState.suspenseyImages;
	}
	var bind = Function.prototype.bind;
	var supportsRequestStorage = "function" === typeof AsyncLocalStorage;
	var requestStorage = supportsRequestStorage ? new AsyncLocalStorage() : null;
	var REACT_CLIENT_REFERENCE = Symbol.for("react.client.reference");
	function getComponentNameFromType(type) {
		if (null == type) return null;
		if ("function" === typeof type) return type.$$typeof === REACT_CLIENT_REFERENCE ? null : type.displayName || type.name || null;
		if ("string" === typeof type) return type;
		switch (type) {
			case REACT_FRAGMENT_TYPE: return "Fragment";
			case REACT_PROFILER_TYPE: return "Profiler";
			case REACT_STRICT_MODE_TYPE: return "StrictMode";
			case REACT_SUSPENSE_TYPE: return "Suspense";
			case REACT_SUSPENSE_LIST_TYPE: return "SuspenseList";
			case REACT_ACTIVITY_TYPE: return "Activity";
		}
		if ("object" === typeof type) switch (type.$$typeof) {
			case REACT_PORTAL_TYPE: return "Portal";
			case REACT_CONTEXT_TYPE: return type.displayName || "Context";
			case REACT_CONSUMER_TYPE: return (type._context.displayName || "Context") + ".Consumer";
			case REACT_FORWARD_REF_TYPE:
				var innerType = type.render;
				type = type.displayName;
				type || (type = innerType.displayName || innerType.name || "", type = "" !== type ? "ForwardRef(" + type + ")" : "ForwardRef");
				return type;
			case REACT_MEMO_TYPE: return innerType = type.displayName || null, null !== innerType ? innerType : getComponentNameFromType(type.type) || "Memo";
			case REACT_LAZY_TYPE:
				innerType = type._payload;
				type = type._init;
				try {
					return getComponentNameFromType(type(innerType));
				} catch (x) {}
		}
		return null;
	}
	var emptyContextObject = {};
	var currentActiveSnapshot = null;
	function popToNearestCommonAncestor(prev, next) {
		if (prev !== next) {
			prev.context._currentValue = prev.parentValue;
			prev = prev.parent;
			var parentNext = next.parent;
			if (null === prev) {
				if (null !== parentNext) throw Error("The stacks must reach the root at the same time. This is a bug in React.");
			} else {
				if (null === parentNext) throw Error("The stacks must reach the root at the same time. This is a bug in React.");
				popToNearestCommonAncestor(prev, parentNext);
			}
			next.context._currentValue = next.value;
		}
	}
	function popAllPrevious(prev) {
		prev.context._currentValue = prev.parentValue;
		prev = prev.parent;
		null !== prev && popAllPrevious(prev);
	}
	function pushAllNext(next) {
		var parentNext = next.parent;
		null !== parentNext && pushAllNext(parentNext);
		next.context._currentValue = next.value;
	}
	function popPreviousToCommonLevel(prev, next) {
		prev.context._currentValue = prev.parentValue;
		prev = prev.parent;
		if (null === prev) throw Error("The depth must equal at least at zero before reaching the root. This is a bug in React.");
		prev.depth === next.depth ? popToNearestCommonAncestor(prev, next) : popPreviousToCommonLevel(prev, next);
	}
	function popNextToCommonLevel(prev, next) {
		var parentNext = next.parent;
		if (null === parentNext) throw Error("The depth must equal at least at zero before reaching the root. This is a bug in React.");
		prev.depth === parentNext.depth ? popToNearestCommonAncestor(prev, parentNext) : popNextToCommonLevel(prev, parentNext);
		next.context._currentValue = next.value;
	}
	function switchContext(newSnapshot) {
		var prev = currentActiveSnapshot;
		prev !== newSnapshot && (null === prev ? pushAllNext(newSnapshot) : null === newSnapshot ? popAllPrevious(prev) : prev.depth === newSnapshot.depth ? popToNearestCommonAncestor(prev, newSnapshot) : prev.depth > newSnapshot.depth ? popPreviousToCommonLevel(prev, newSnapshot) : popNextToCommonLevel(prev, newSnapshot), currentActiveSnapshot = newSnapshot);
	}
	var classComponentUpdater = {
		enqueueSetState: function(inst, payload) {
			inst = inst._reactInternals;
			null !== inst.queue && inst.queue.push(payload);
		},
		enqueueReplaceState: function(inst, payload) {
			inst = inst._reactInternals;
			inst.replace = !0;
			inst.queue = [payload];
		},
		enqueueForceUpdate: function() {}
	};
	var emptyTreeContext = {
		id: 1,
		overflow: ""
	};
	function pushTreeContext(baseContext, totalChildren, index) {
		var baseIdWithLeadingBit = baseContext.id;
		baseContext = baseContext.overflow;
		var baseLength = 32 - clz32(baseIdWithLeadingBit) - 1;
		baseIdWithLeadingBit &= ~(1 << baseLength);
		index += 1;
		var length = 32 - clz32(totalChildren) + baseLength;
		if (30 < length) {
			var numberOfOverflowBits = baseLength - baseLength % 5;
			length = (baseIdWithLeadingBit & (1 << numberOfOverflowBits) - 1).toString(32);
			baseIdWithLeadingBit >>= numberOfOverflowBits;
			baseLength -= numberOfOverflowBits;
			return {
				id: 1 << 32 - clz32(totalChildren) + baseLength | index << baseLength | baseIdWithLeadingBit,
				overflow: length + baseContext
			};
		}
		return {
			id: 1 << length | index << baseLength | baseIdWithLeadingBit,
			overflow: baseContext
		};
	}
	var clz32 = Math.clz32 ? Math.clz32 : clz32Fallback;
	var log = Math.log;
	var LN2 = Math.LN2;
	function clz32Fallback(x) {
		x >>>= 0;
		return 0 === x ? 32 : 31 - (log(x) / LN2 | 0) | 0;
	}
	function noop() {}
	var SuspenseException = Error("Suspense Exception: This is not a real error! It's an implementation detail of `use` to interrupt the current render. You must either rethrow it immediately, or move the `use` call outside of the `try/catch` block. Capturing without rethrowing will lead to unexpected behavior.\n\nTo handle async errors, wrap your component in an error boundary, or call the promise's `.catch` method and pass the result to `use`.");
	function trackUsedThenable(thenableState, thenable, index) {
		index = thenableState[index];
		void 0 === index ? thenableState.push(thenable) : index !== thenable && (thenable.then(noop, noop), thenable = index);
		switch (thenable.status) {
			case "fulfilled": return thenable.value;
			case "rejected": throw thenable.reason;
			default:
				"string" === typeof thenable.status ? thenable.then(noop, noop) : (thenableState = thenable, thenableState.status = "pending", thenableState.then(function(fulfilledValue) {
					if ("pending" === thenable.status) {
						var fulfilledThenable = thenable;
						fulfilledThenable.status = "fulfilled";
						fulfilledThenable.value = fulfilledValue;
					}
				}, function(error) {
					if ("pending" === thenable.status) {
						var rejectedThenable = thenable;
						rejectedThenable.status = "rejected";
						rejectedThenable.reason = error;
					}
				}));
				switch (thenable.status) {
					case "fulfilled": return thenable.value;
					case "rejected": throw thenable.reason;
				}
				suspendedThenable = thenable;
				throw SuspenseException;
		}
	}
	var suspendedThenable = null;
	function getSuspendedThenable() {
		if (null === suspendedThenable) throw Error("Expected a suspended thenable. This is a bug in React. Please file an issue.");
		var thenable = suspendedThenable;
		suspendedThenable = null;
		return thenable;
	}
	function is(x, y) {
		return x === y && (0 !== x || 1 / x === 1 / y) || x !== x && y !== y;
	}
	var objectIs = "function" === typeof Object.is ? Object.is : is;
	var currentlyRenderingComponent = null;
	var currentlyRenderingTask = null;
	var currentlyRenderingRequest = null;
	var currentlyRenderingKeyPath = null;
	var firstWorkInProgressHook = null;
	var workInProgressHook = null;
	var isReRender = !1;
	var didScheduleRenderPhaseUpdate = !1;
	var localIdCounter = 0;
	var actionStateCounter = 0;
	var actionStateMatchingIndex = -1;
	var thenableIndexCounter = 0;
	var thenableState = null;
	var renderPhaseUpdates = null;
	var numberOfReRenders = 0;
	function resolveCurrentlyRenderingComponent() {
		if (null === currentlyRenderingComponent) throw Error("Invalid hook call. Hooks can only be called inside of the body of a function component. This could happen for one of the following reasons:\n1. You might have mismatching versions of React and the renderer (such as React DOM)\n2. You might be breaking the Rules of Hooks\n3. You might have more than one copy of React in the same app\nSee https://react.dev/link/invalid-hook-call for tips about how to debug and fix this problem.");
		return currentlyRenderingComponent;
	}
	function createHook() {
		if (0 < numberOfReRenders) throw Error("Rendered more hooks than during the previous render");
		return {
			memoizedState: null,
			queue: null,
			next: null
		};
	}
	function createWorkInProgressHook() {
		null === workInProgressHook ? null === firstWorkInProgressHook ? (isReRender = !1, firstWorkInProgressHook = workInProgressHook = createHook()) : (isReRender = !0, workInProgressHook = firstWorkInProgressHook) : null === workInProgressHook.next ? (isReRender = !1, workInProgressHook = workInProgressHook.next = createHook()) : (isReRender = !0, workInProgressHook = workInProgressHook.next);
		return workInProgressHook;
	}
	function getThenableStateAfterSuspending() {
		var state = thenableState;
		thenableState = null;
		return state;
	}
	function resetHooksState() {
		currentlyRenderingKeyPath = currentlyRenderingRequest = currentlyRenderingTask = currentlyRenderingComponent = null;
		didScheduleRenderPhaseUpdate = !1;
		firstWorkInProgressHook = null;
		numberOfReRenders = 0;
		workInProgressHook = renderPhaseUpdates = null;
	}
	function basicStateReducer(state, action) {
		return "function" === typeof action ? action(state) : action;
	}
	function useReducer(reducer, initialArg, init) {
		currentlyRenderingComponent = resolveCurrentlyRenderingComponent();
		workInProgressHook = createWorkInProgressHook();
		if (isReRender) {
			var queue = workInProgressHook.queue;
			initialArg = queue.dispatch;
			if (null !== renderPhaseUpdates && (init = renderPhaseUpdates.get(queue), void 0 !== init)) {
				renderPhaseUpdates.delete(queue);
				queue = workInProgressHook.memoizedState;
				do
					queue = reducer(queue, init.action), init = init.next;
				while (null !== init);
				workInProgressHook.memoizedState = queue;
				return [queue, initialArg];
			}
			return [workInProgressHook.memoizedState, initialArg];
		}
		reducer = reducer === basicStateReducer ? "function" === typeof initialArg ? initialArg() : initialArg : void 0 !== init ? init(initialArg) : initialArg;
		workInProgressHook.memoizedState = reducer;
		reducer = workInProgressHook.queue = {
			last: null,
			dispatch: null
		};
		reducer = reducer.dispatch = dispatchAction.bind(null, currentlyRenderingComponent, reducer);
		return [workInProgressHook.memoizedState, reducer];
	}
	function useMemo(nextCreate, deps) {
		currentlyRenderingComponent = resolveCurrentlyRenderingComponent();
		workInProgressHook = createWorkInProgressHook();
		deps = void 0 === deps ? null : deps;
		if (null !== workInProgressHook) {
			var prevState = workInProgressHook.memoizedState;
			if (null !== prevState && null !== deps) {
				var prevDeps = prevState[1];
				a: if (null === prevDeps) prevDeps = !1;
				else {
					for (var i = 0; i < prevDeps.length && i < deps.length; i++) if (!objectIs(deps[i], prevDeps[i])) {
						prevDeps = !1;
						break a;
					}
					prevDeps = !0;
				}
				if (prevDeps) return prevState[0];
			}
		}
		nextCreate = nextCreate();
		workInProgressHook.memoizedState = [nextCreate, deps];
		return nextCreate;
	}
	function dispatchAction(componentIdentity, queue, action) {
		if (25 <= numberOfReRenders) throw Error("Too many re-renders. React limits the number of renders to prevent an infinite loop.");
		if (componentIdentity === currentlyRenderingComponent) if (didScheduleRenderPhaseUpdate = !0, componentIdentity = {
			action,
			next: null
		}, null === renderPhaseUpdates && (renderPhaseUpdates = /* @__PURE__ */ new Map()), action = renderPhaseUpdates.get(queue), void 0 === action) renderPhaseUpdates.set(queue, componentIdentity);
		else {
			for (queue = action; null !== queue.next;) queue = queue.next;
			queue.next = componentIdentity;
		}
	}
	function throwOnUseEffectEventCall() {
		throw Error("A function wrapped in useEffectEvent can't be called during rendering.");
	}
	function unsupportedStartTransition() {
		throw Error("startTransition cannot be called during server rendering.");
	}
	function unsupportedSetOptimisticState() {
		throw Error("Cannot update optimistic state while rendering.");
	}
	function useActionState(action, initialState, permalink) {
		resolveCurrentlyRenderingComponent();
		var actionStateHookIndex = actionStateCounter++, request = currentlyRenderingRequest;
		if ("function" === typeof action.$$FORM_ACTION) {
			var nextPostbackStateKey = null, componentKeyPath = currentlyRenderingKeyPath;
			request = request.formState;
			var isSignatureEqual = action.$$IS_SIGNATURE_EQUAL;
			if (null !== request && "function" === typeof isSignatureEqual) {
				var postbackKey = request[1];
				isSignatureEqual.call(action, request[2], request[3]) && (nextPostbackStateKey = void 0 !== permalink ? "p" + permalink : "k" + murmurhash3_32_gc(JSON.stringify([
					componentKeyPath,
					null,
					actionStateHookIndex
				]), 0), postbackKey === nextPostbackStateKey && (actionStateMatchingIndex = actionStateHookIndex, initialState = request[0]));
			}
			var boundAction = action.bind(null, initialState);
			action = function(payload) {
				boundAction(payload);
			};
			"function" === typeof boundAction.$$FORM_ACTION && (action.$$FORM_ACTION = function(prefix) {
				prefix = boundAction.$$FORM_ACTION(prefix);
				void 0 !== permalink && (permalink += "", prefix.action = permalink);
				var formData = prefix.data;
				formData && (null === nextPostbackStateKey && (nextPostbackStateKey = void 0 !== permalink ? "p" + permalink : "k" + murmurhash3_32_gc(JSON.stringify([
					componentKeyPath,
					null,
					actionStateHookIndex
				]), 0)), formData.append("$ACTION_KEY", nextPostbackStateKey));
				return prefix;
			});
			return [
				initialState,
				action,
				!1
			];
		}
		var boundAction$22 = action.bind(null, initialState);
		return [
			initialState,
			function(payload) {
				boundAction$22(payload);
			},
			!1
		];
	}
	function unwrapThenable(thenable) {
		var index = thenableIndexCounter;
		thenableIndexCounter += 1;
		null === thenableState && (thenableState = []);
		return trackUsedThenable(thenableState, thenable, index);
	}
	function unsupportedRefresh() {
		throw Error("Cache cannot be refreshed during server rendering.");
	}
	var HooksDispatcher = {
		readContext: function(context) {
			return context._currentValue;
		},
		use: function(usable) {
			if (null !== usable && "object" === typeof usable) {
				if ("function" === typeof usable.then) return unwrapThenable(usable);
				if (usable.$$typeof === REACT_CONTEXT_TYPE) return usable._currentValue;
			}
			throw Error("An unsupported type was passed to use(): " + String(usable));
		},
		useContext: function(context) {
			resolveCurrentlyRenderingComponent();
			return context._currentValue;
		},
		useMemo,
		useReducer,
		useRef: function(initialValue) {
			currentlyRenderingComponent = resolveCurrentlyRenderingComponent();
			workInProgressHook = createWorkInProgressHook();
			var previousRef = workInProgressHook.memoizedState;
			return null === previousRef ? (initialValue = { current: initialValue }, workInProgressHook.memoizedState = initialValue) : previousRef;
		},
		useState: function(initialState) {
			return useReducer(basicStateReducer, initialState);
		},
		useInsertionEffect: noop,
		useLayoutEffect: noop,
		useCallback: function(callback, deps) {
			return useMemo(function() {
				return callback;
			}, deps);
		},
		useImperativeHandle: noop,
		useEffect: noop,
		useDebugValue: noop,
		useDeferredValue: function(value, initialValue) {
			resolveCurrentlyRenderingComponent();
			return void 0 !== initialValue ? initialValue : value;
		},
		useTransition: function() {
			resolveCurrentlyRenderingComponent();
			return [!1, unsupportedStartTransition];
		},
		useId: function() {
			var JSCompiler_inline_result = currentlyRenderingTask.treeContext;
			var overflow = JSCompiler_inline_result.overflow;
			JSCompiler_inline_result = JSCompiler_inline_result.id;
			JSCompiler_inline_result = (JSCompiler_inline_result & ~(1 << 32 - clz32(JSCompiler_inline_result) - 1)).toString(32) + overflow;
			var resumableState = currentResumableState;
			if (null === resumableState) throw Error("Invalid hook call. Hooks can only be called inside of the body of a function component.");
			overflow = localIdCounter++;
			JSCompiler_inline_result = "_" + resumableState.idPrefix + "R_" + JSCompiler_inline_result;
			0 < overflow && (JSCompiler_inline_result += "H" + overflow.toString(32));
			return JSCompiler_inline_result + "_";
		},
		useSyncExternalStore: function(subscribe, getSnapshot, getServerSnapshot) {
			if (void 0 === getServerSnapshot) throw Error("Missing getServerSnapshot, which is required for server-rendered content. Will revert to client rendering.");
			return getServerSnapshot();
		},
		useOptimistic: function(passthrough) {
			resolveCurrentlyRenderingComponent();
			return [passthrough, unsupportedSetOptimisticState];
		},
		useActionState,
		useFormState: useActionState,
		useHostTransitionStatus: function() {
			resolveCurrentlyRenderingComponent();
			return sharedNotPendingObject;
		},
		useMemoCache: function(size) {
			for (var data = Array(size), i = 0; i < size; i++) data[i] = REACT_MEMO_CACHE_SENTINEL;
			return data;
		},
		useCacheRefresh: function() {
			return unsupportedRefresh;
		},
		useEffectEvent: function() {
			return throwOnUseEffectEventCall;
		}
	};
	var currentResumableState = null;
	var DefaultAsyncDispatcher = {
		getCacheForType: function() {
			throw Error("Not implemented.");
		},
		cacheSignal: function() {
			throw Error("Not implemented.");
		}
	};
	function prepareStackTrace(error, structuredStackTrace) {
		error = (error.name || "Error") + ": " + (error.message || "");
		for (var i = 0; i < structuredStackTrace.length; i++) error += "\n    at " + structuredStackTrace[i].toString();
		return error;
	}
	var prefix;
	var suffix;
	function describeBuiltInComponentFrame(name) {
		if (void 0 === prefix) try {
			throw Error();
		} catch (x) {
			var match = x.stack.trim().match(/\n( *(at )?)/);
			prefix = match && match[1] || "";
			suffix = -1 < x.stack.indexOf("\n    at") ? " (<anonymous>)" : -1 < x.stack.indexOf("@") ? "@unknown:0:0" : "";
		}
		return "\n" + prefix + name + suffix;
	}
	var reentry = !1;
	function describeNativeComponentFrame(fn, construct) {
		if (!fn || reentry) return "";
		reentry = !0;
		var previousPrepareStackTrace = Error.prepareStackTrace;
		Error.prepareStackTrace = prepareStackTrace;
		try {
			var RunInRootFrame = { DetermineComponentFrameRoot: function() {
				try {
					if (construct) {
						var Fake = function() {
							throw Error();
						};
						Object.defineProperty(Fake.prototype, "props", { set: function() {
							throw Error();
						} });
						if ("object" === typeof Reflect && Reflect.construct) {
							try {
								Reflect.construct(Fake, []);
							} catch (x) {
								var control = x;
							}
							Reflect.construct(fn, [], Fake);
						} else {
							try {
								Fake.call();
							} catch (x$24) {
								control = x$24;
							}
							fn.call(Fake.prototype);
						}
					} else {
						try {
							throw Error();
						} catch (x$25) {
							control = x$25;
						}
						(Fake = fn()) && "function" === typeof Fake.catch && Fake.catch(function() {});
					}
				} catch (sample) {
					if (sample && control && "string" === typeof sample.stack) return [sample.stack, control.stack];
				}
				return [null, null];
			} };
			RunInRootFrame.DetermineComponentFrameRoot.displayName = "DetermineComponentFrameRoot";
			var namePropDescriptor = Object.getOwnPropertyDescriptor(RunInRootFrame.DetermineComponentFrameRoot, "name");
			namePropDescriptor && namePropDescriptor.configurable && Object.defineProperty(RunInRootFrame.DetermineComponentFrameRoot, "name", { value: "DetermineComponentFrameRoot" });
			var _RunInRootFrame$Deter = RunInRootFrame.DetermineComponentFrameRoot(), sampleStack = _RunInRootFrame$Deter[0], controlStack = _RunInRootFrame$Deter[1];
			if (sampleStack && controlStack) {
				var sampleLines = sampleStack.split("\n"), controlLines = controlStack.split("\n");
				for (namePropDescriptor = RunInRootFrame = 0; RunInRootFrame < sampleLines.length && !sampleLines[RunInRootFrame].includes("DetermineComponentFrameRoot");) RunInRootFrame++;
				for (; namePropDescriptor < controlLines.length && !controlLines[namePropDescriptor].includes("DetermineComponentFrameRoot");) namePropDescriptor++;
				if (RunInRootFrame === sampleLines.length || namePropDescriptor === controlLines.length) for (RunInRootFrame = sampleLines.length - 1, namePropDescriptor = controlLines.length - 1; 1 <= RunInRootFrame && 0 <= namePropDescriptor && sampleLines[RunInRootFrame] !== controlLines[namePropDescriptor];) namePropDescriptor--;
				for (; 1 <= RunInRootFrame && 0 <= namePropDescriptor; RunInRootFrame--, namePropDescriptor--) if (sampleLines[RunInRootFrame] !== controlLines[namePropDescriptor]) {
					if (1 !== RunInRootFrame || 1 !== namePropDescriptor) do
						if (RunInRootFrame--, namePropDescriptor--, 0 > namePropDescriptor || sampleLines[RunInRootFrame] !== controlLines[namePropDescriptor]) {
							var frame = "\n" + sampleLines[RunInRootFrame].replace(" at new ", " at ");
							fn.displayName && frame.includes("<anonymous>") && (frame = frame.replace("<anonymous>", fn.displayName));
							return frame;
						}
					while (1 <= RunInRootFrame && 0 <= namePropDescriptor);
					break;
				}
			}
		} finally {
			reentry = !1, Error.prepareStackTrace = previousPrepareStackTrace;
		}
		return (previousPrepareStackTrace = fn ? fn.displayName || fn.name : "") ? describeBuiltInComponentFrame(previousPrepareStackTrace) : "";
	}
	function describeComponentStackByType(type) {
		if ("string" === typeof type) return describeBuiltInComponentFrame(type);
		if ("function" === typeof type) return type.prototype && type.prototype.isReactComponent ? describeNativeComponentFrame(type, !0) : describeNativeComponentFrame(type, !1);
		if ("object" === typeof type && null !== type) {
			switch (type.$$typeof) {
				case REACT_FORWARD_REF_TYPE: return describeNativeComponentFrame(type.render, !1);
				case REACT_MEMO_TYPE: return describeNativeComponentFrame(type.type, !1);
				case REACT_LAZY_TYPE:
					var lazyComponent = type, payload = lazyComponent._payload;
					lazyComponent = lazyComponent._init;
					try {
						type = lazyComponent(payload);
					} catch (x) {
						return describeBuiltInComponentFrame("Lazy");
					}
					return describeComponentStackByType(type);
			}
			if ("string" === typeof type.name) {
				a: {
					payload = type.name;
					lazyComponent = type.env;
					var location = type.debugLocation;
					if (null != location && (type = Error.prepareStackTrace, Error.prepareStackTrace = prepareStackTrace, location = location.stack, Error.prepareStackTrace = type, location.startsWith("Error: react-stack-top-frame\n") && (location = location.slice(29)), type = location.indexOf("\n"), -1 !== type && (location = location.slice(type + 1)), type = location.indexOf("react_stack_bottom_frame"), -1 !== type && (type = location.lastIndexOf("\n", type)), type = -1 !== type ? location = location.slice(0, type) : "", location = type.lastIndexOf("\n"), type = -1 === location ? type : type.slice(location + 1), -1 !== type.indexOf(payload))) {
						payload = "\n" + type;
						break a;
					}
					payload = describeBuiltInComponentFrame(payload + (lazyComponent ? " [" + lazyComponent + "]" : ""));
				}
				return payload;
			}
		}
		switch (type) {
			case REACT_SUSPENSE_LIST_TYPE: return describeBuiltInComponentFrame("SuspenseList");
			case REACT_SUSPENSE_TYPE: return describeBuiltInComponentFrame("Suspense");
		}
		return "";
	}
	function isEligibleForOutlining(request, boundary) {
		return (500 < boundary.byteSize || hasSuspenseyContent(boundary.contentState)) && null === boundary.contentPreamble;
	}
	function defaultErrorHandler(error) {
		if ("object" === typeof error && null !== error && "string" === typeof error.environmentName) {
			var JSCompiler_inline_result = error.environmentName;
			error = [error].slice(0);
			"string" === typeof error[0] ? error.splice(0, 1, "\x1B[0m\x1B[7m%c%s\x1B[0m%c " + error[0], "background: #e6e6e6;background: light-dark(rgba(0,0,0,0.1), rgba(255,255,255,0.25));color: #000000;color: light-dark(#000000, #ffffff);border-radius: 2px", " " + JSCompiler_inline_result + " ", "") : error.splice(0, 0, "\x1B[0m\x1B[7m%c%s\x1B[0m%c", "background: #e6e6e6;background: light-dark(rgba(0,0,0,0.1), rgba(255,255,255,0.25));color: #000000;color: light-dark(#000000, #ffffff);border-radius: 2px", " " + JSCompiler_inline_result + " ", "");
			error.unshift(console);
			JSCompiler_inline_result = bind.apply(console.error, error);
			JSCompiler_inline_result();
		} else console.error(error);
		return null;
	}
	function RequestInstance(resumableState, renderState, rootFormatContext, progressiveChunkSize, onError, onAllReady, onShellReady, onShellError, onFatalError, onPostpone, formState) {
		var abortSet = /* @__PURE__ */ new Set();
		this.destination = null;
		this.flushScheduled = !1;
		this.resumableState = resumableState;
		this.renderState = renderState;
		this.rootFormatContext = rootFormatContext;
		this.progressiveChunkSize = void 0 === progressiveChunkSize ? 12800 : progressiveChunkSize;
		this.status = 10;
		this.fatalError = null;
		this.pendingRootTasks = this.allPendingTasks = this.nextSegmentId = 0;
		this.completedPreambleSegments = this.completedRootSegment = null;
		this.byteSize = 0;
		this.abortableTasks = abortSet;
		this.pingedTasks = [];
		this.clientRenderedBoundaries = [];
		this.completedBoundaries = [];
		this.partialBoundaries = [];
		this.trackedPostpones = null;
		this.onError = void 0 === onError ? defaultErrorHandler : onError;
		this.onPostpone = void 0 === onPostpone ? noop : onPostpone;
		this.onAllReady = void 0 === onAllReady ? noop : onAllReady;
		this.onShellReady = void 0 === onShellReady ? noop : onShellReady;
		this.onShellError = void 0 === onShellError ? noop : onShellError;
		this.onFatalError = void 0 === onFatalError ? noop : onFatalError;
		this.formState = void 0 === formState ? null : formState;
	}
	function createRequest(children, resumableState, renderState, rootFormatContext, progressiveChunkSize, onError, onAllReady, onShellReady, onShellError, onFatalError, onPostpone, formState) {
		resumableState = new RequestInstance(resumableState, renderState, rootFormatContext, progressiveChunkSize, onError, onAllReady, onShellReady, onShellError, onFatalError, onPostpone, formState);
		renderState = createPendingSegment(resumableState, 0, null, rootFormatContext, !1, !1);
		renderState.parentFlushed = !0;
		children = createRenderTask(resumableState, null, children, -1, null, renderState, null, null, resumableState.abortableTasks, null, rootFormatContext, null, emptyTreeContext, null, null);
		pushComponentStack(children);
		resumableState.pingedTasks.push(children);
		return resumableState;
	}
	function createPrerenderRequest(children, resumableState, renderState, rootFormatContext, progressiveChunkSize, onError, onAllReady, onShellReady, onShellError, onFatalError, onPostpone) {
		children = createRequest(children, resumableState, renderState, rootFormatContext, progressiveChunkSize, onError, onAllReady, onShellReady, onShellError, onFatalError, onPostpone, void 0);
		children.trackedPostpones = {
			workingMap: /* @__PURE__ */ new Map(),
			rootNodes: [],
			rootSlots: null
		};
		return children;
	}
	function resumeRequest(children, postponedState, renderState, onError, onAllReady, onShellReady, onShellError, onFatalError, onPostpone) {
		renderState = new RequestInstance(postponedState.resumableState, renderState, postponedState.rootFormatContext, postponedState.progressiveChunkSize, onError, onAllReady, onShellReady, onShellError, onFatalError, onPostpone, null);
		renderState.nextSegmentId = postponedState.nextSegmentId;
		if ("number" === typeof postponedState.replaySlots) return onError = createPendingSegment(renderState, 0, null, postponedState.rootFormatContext, !1, !1), onError.parentFlushed = !0, children = createRenderTask(renderState, null, children, -1, null, onError, null, null, renderState.abortableTasks, null, postponedState.rootFormatContext, null, emptyTreeContext, null, null), pushComponentStack(children), renderState.pingedTasks.push(children), renderState;
		children = createReplayTask(renderState, null, {
			nodes: postponedState.replayNodes,
			slots: postponedState.replaySlots,
			pendingTasks: 0
		}, children, -1, null, null, renderState.abortableTasks, null, postponedState.rootFormatContext, null, emptyTreeContext, null, null);
		pushComponentStack(children);
		renderState.pingedTasks.push(children);
		return renderState;
	}
	function resumeAndPrerenderRequest(children, postponedState, renderState, onError, onAllReady, onShellReady, onShellError, onFatalError, onPostpone) {
		children = resumeRequest(children, postponedState, renderState, onError, onAllReady, onShellReady, onShellError, onFatalError, onPostpone);
		children.trackedPostpones = {
			workingMap: /* @__PURE__ */ new Map(),
			rootNodes: [],
			rootSlots: null
		};
		return children;
	}
	var currentRequest = null;
	function resolveRequest() {
		if (currentRequest) return currentRequest;
		if (supportsRequestStorage) {
			var store = requestStorage.getStore();
			if (store) return store;
		}
		return null;
	}
	function pingTask(request, task) {
		request.pingedTasks.push(task);
		1 === request.pingedTasks.length && (request.flushScheduled = null !== request.destination, null !== request.trackedPostpones || 10 === request.status ? scheduleMicrotask(function() {
			return performWork(request);
		}) : setTimeout(function() {
			return performWork(request);
		}, 0));
	}
	function createSuspenseBoundary(request, row, fallbackAbortableTasks, contentPreamble, fallbackPreamble) {
		fallbackAbortableTasks = {
			status: 0,
			rootSegmentID: -1,
			parentFlushed: !1,
			pendingTasks: 0,
			row,
			completedSegments: [],
			byteSize: 0,
			fallbackAbortableTasks,
			errorDigest: null,
			contentState: createHoistableState(),
			fallbackState: createHoistableState(),
			contentPreamble,
			fallbackPreamble,
			trackedContentKeyPath: null,
			trackedFallbackNode: null
		};
		null !== row && (row.pendingTasks++, contentPreamble = row.boundaries, null !== contentPreamble && (request.allPendingTasks++, fallbackAbortableTasks.pendingTasks++, contentPreamble.push(fallbackAbortableTasks)), request = row.inheritedHoistables, null !== request && hoistHoistables(fallbackAbortableTasks.contentState, request));
		return fallbackAbortableTasks;
	}
	function createRenderTask(request, thenableState, node, childIndex, blockedBoundary, blockedSegment, blockedPreamble, hoistableState, abortSet, keyPath, formatContext, context, treeContext, row, componentStack) {
		request.allPendingTasks++;
		null === blockedBoundary ? request.pendingRootTasks++ : blockedBoundary.pendingTasks++;
		null !== row && row.pendingTasks++;
		var task = {
			replay: null,
			node,
			childIndex,
			ping: function() {
				return pingTask(request, task);
			},
			blockedBoundary,
			blockedSegment,
			blockedPreamble,
			hoistableState,
			abortSet,
			keyPath,
			formatContext,
			context,
			treeContext,
			row,
			componentStack,
			thenableState
		};
		abortSet.add(task);
		return task;
	}
	function createReplayTask(request, thenableState, replay, node, childIndex, blockedBoundary, hoistableState, abortSet, keyPath, formatContext, context, treeContext, row, componentStack) {
		request.allPendingTasks++;
		null === blockedBoundary ? request.pendingRootTasks++ : blockedBoundary.pendingTasks++;
		null !== row && row.pendingTasks++;
		replay.pendingTasks++;
		var task = {
			replay,
			node,
			childIndex,
			ping: function() {
				return pingTask(request, task);
			},
			blockedBoundary,
			blockedSegment: null,
			blockedPreamble: null,
			hoistableState,
			abortSet,
			keyPath,
			formatContext,
			context,
			treeContext,
			row,
			componentStack,
			thenableState
		};
		abortSet.add(task);
		return task;
	}
	function createPendingSegment(request, index, boundary, parentFormatContext, lastPushedText, textEmbedded) {
		return {
			status: 0,
			parentFlushed: !1,
			id: -1,
			index,
			chunks: [],
			children: [],
			preambleChildren: [],
			parentFormatContext,
			boundary,
			lastPushedText,
			textEmbedded
		};
	}
	function pushComponentStack(task) {
		var node = task.node;
		if ("object" === typeof node && null !== node) switch (node.$$typeof) {
			case REACT_ELEMENT_TYPE: task.componentStack = {
				parent: task.componentStack,
				type: node.type
			};
		}
	}
	function replaceSuspenseComponentStackWithSuspenseFallbackStack(componentStack) {
		return null === componentStack ? null : {
			parent: componentStack.parent,
			type: "Suspense Fallback"
		};
	}
	function getThrownInfo(node$jscomp$0) {
		var errorInfo = {};
		node$jscomp$0 && Object.defineProperty(errorInfo, "componentStack", {
			configurable: !0,
			enumerable: !0,
			get: function() {
				try {
					var info = "", node = node$jscomp$0;
					do
						info += describeComponentStackByType(node.type), node = node.parent;
					while (node);
					var JSCompiler_inline_result = info;
				} catch (x) {
					JSCompiler_inline_result = "\nError generating stack: " + x.message + "\n" + x.stack;
				}
				Object.defineProperty(errorInfo, "componentStack", { value: JSCompiler_inline_result });
				return JSCompiler_inline_result;
			}
		});
		return errorInfo;
	}
	function logRecoverableError(request, error, errorInfo) {
		request = request.onError;
		error = request(error, errorInfo);
		if (null == error || "string" === typeof error) return error;
	}
	function fatalError(request, error) {
		var onShellError = request.onShellError, onFatalError = request.onFatalError;
		onShellError(error);
		onFatalError(error);
		null !== request.destination ? (request.status = 14, closeWithError(request.destination, error)) : (request.status = 13, request.fatalError = error);
	}
	function finishSuspenseListRow(request, row) {
		unblockSuspenseListRow(request, row.next, row.hoistables);
	}
	function unblockSuspenseListRow(request, unblockedRow, inheritedHoistables) {
		for (; null !== unblockedRow;) {
			null !== inheritedHoistables && (hoistHoistables(unblockedRow.hoistables, inheritedHoistables), unblockedRow.inheritedHoistables = inheritedHoistables);
			var unblockedBoundaries = unblockedRow.boundaries;
			if (null !== unblockedBoundaries) {
				unblockedRow.boundaries = null;
				for (var i = 0; i < unblockedBoundaries.length; i++) {
					var unblockedBoundary = unblockedBoundaries[i];
					null !== inheritedHoistables && hoistHoistables(unblockedBoundary.contentState, inheritedHoistables);
					finishedTask(request, unblockedBoundary, null, null);
				}
			}
			unblockedRow.pendingTasks--;
			if (0 < unblockedRow.pendingTasks) break;
			inheritedHoistables = unblockedRow.hoistables;
			unblockedRow = unblockedRow.next;
		}
	}
	function tryToResolveTogetherRow(request, togetherRow) {
		var boundaries = togetherRow.boundaries;
		if (null !== boundaries && togetherRow.pendingTasks === boundaries.length) {
			for (var allCompleteAndInlinable = !0, i = 0; i < boundaries.length; i++) {
				var rowBoundary = boundaries[i];
				if (1 !== rowBoundary.pendingTasks || rowBoundary.parentFlushed || isEligibleForOutlining(request, rowBoundary)) {
					allCompleteAndInlinable = !1;
					break;
				}
			}
			allCompleteAndInlinable && unblockSuspenseListRow(request, togetherRow, togetherRow.hoistables);
		}
	}
	function createSuspenseListRow(previousRow) {
		var newRow = {
			pendingTasks: 1,
			boundaries: null,
			hoistables: createHoistableState(),
			inheritedHoistables: null,
			together: !1,
			next: null
		};
		null !== previousRow && 0 < previousRow.pendingTasks && (newRow.pendingTasks++, newRow.boundaries = [], previousRow.next = newRow);
		return newRow;
	}
	function renderSuspenseListRows(request, task, keyPath, rows, revealOrder) {
		var prevKeyPath = task.keyPath, prevTreeContext = task.treeContext, prevRow = task.row;
		task.keyPath = keyPath;
		keyPath = rows.length;
		var previousSuspenseListRow = null;
		if (null !== task.replay) {
			var resumeSlots = task.replay.slots;
			if (null !== resumeSlots && "object" === typeof resumeSlots) for (var n = 0; n < keyPath; n++) {
				var i = "backwards" !== revealOrder && "unstable_legacy-backwards" !== revealOrder ? n : keyPath - 1 - n, node = rows[i];
				task.row = previousSuspenseListRow = createSuspenseListRow(previousSuspenseListRow);
				task.treeContext = pushTreeContext(prevTreeContext, keyPath, i);
				var resumeSegmentID = resumeSlots[i];
				"number" === typeof resumeSegmentID ? (resumeNode(request, task, resumeSegmentID, node, i), delete resumeSlots[i]) : renderNode(request, task, node, i);
				0 === --previousSuspenseListRow.pendingTasks && finishSuspenseListRow(request, previousSuspenseListRow);
			}
			else for (resumeSlots = 0; resumeSlots < keyPath; resumeSlots++) n = "backwards" !== revealOrder && "unstable_legacy-backwards" !== revealOrder ? resumeSlots : keyPath - 1 - resumeSlots, i = rows[n], task.row = previousSuspenseListRow = createSuspenseListRow(previousSuspenseListRow), task.treeContext = pushTreeContext(prevTreeContext, keyPath, n), renderNode(request, task, i, n), 0 === --previousSuspenseListRow.pendingTasks && finishSuspenseListRow(request, previousSuspenseListRow);
		} else if ("backwards" !== revealOrder && "unstable_legacy-backwards" !== revealOrder) for (revealOrder = 0; revealOrder < keyPath; revealOrder++) resumeSlots = rows[revealOrder], task.row = previousSuspenseListRow = createSuspenseListRow(previousSuspenseListRow), task.treeContext = pushTreeContext(prevTreeContext, keyPath, revealOrder), renderNode(request, task, resumeSlots, revealOrder), 0 === --previousSuspenseListRow.pendingTasks && finishSuspenseListRow(request, previousSuspenseListRow);
		else {
			revealOrder = task.blockedSegment;
			resumeSlots = revealOrder.children.length;
			n = revealOrder.chunks.length;
			for (i = keyPath - 1; 0 <= i; i--) {
				node = rows[i];
				task.row = previousSuspenseListRow = createSuspenseListRow(previousSuspenseListRow);
				task.treeContext = pushTreeContext(prevTreeContext, keyPath, i);
				resumeSegmentID = createPendingSegment(request, n, null, task.formatContext, 0 === i ? revealOrder.lastPushedText : !0, !0);
				revealOrder.children.splice(resumeSlots, 0, resumeSegmentID);
				task.blockedSegment = resumeSegmentID;
				try {
					renderNode(request, task, node, i), resumeSegmentID.lastPushedText && resumeSegmentID.textEmbedded && resumeSegmentID.chunks.push(textSeparator), resumeSegmentID.status = 1, finishedSegment(request, task.blockedBoundary, resumeSegmentID), 0 === --previousSuspenseListRow.pendingTasks && finishSuspenseListRow(request, previousSuspenseListRow);
				} catch (thrownValue) {
					throw resumeSegmentID.status = 12 === request.status ? 3 : 4, thrownValue;
				}
			}
			task.blockedSegment = revealOrder;
			revealOrder.lastPushedText = !1;
		}
		null !== prevRow && null !== previousSuspenseListRow && 0 < previousSuspenseListRow.pendingTasks && (prevRow.pendingTasks++, previousSuspenseListRow.next = prevRow);
		task.treeContext = prevTreeContext;
		task.row = prevRow;
		task.keyPath = prevKeyPath;
	}
	function renderWithHooks(request, task, keyPath, Component, props, secondArg) {
		var prevThenableState = task.thenableState;
		task.thenableState = null;
		currentlyRenderingComponent = {};
		currentlyRenderingTask = task;
		currentlyRenderingRequest = request;
		currentlyRenderingKeyPath = keyPath;
		actionStateCounter = localIdCounter = 0;
		actionStateMatchingIndex = -1;
		thenableIndexCounter = 0;
		thenableState = prevThenableState;
		for (request = Component(props, secondArg); didScheduleRenderPhaseUpdate;) didScheduleRenderPhaseUpdate = !1, actionStateCounter = localIdCounter = 0, actionStateMatchingIndex = -1, thenableIndexCounter = 0, numberOfReRenders += 1, workInProgressHook = null, request = Component(props, secondArg);
		resetHooksState();
		return request;
	}
	function finishFunctionComponent(request, task, keyPath, children, hasId, actionStateCount, actionStateMatchingIndex) {
		var didEmitActionStateMarkers = !1;
		if (0 !== actionStateCount && null !== request.formState) {
			var segment = task.blockedSegment;
			if (null !== segment) {
				didEmitActionStateMarkers = !0;
				segment = segment.chunks;
				for (var i = 0; i < actionStateCount; i++) i === actionStateMatchingIndex ? segment.push(formStateMarkerIsMatching) : segment.push(formStateMarkerIsNotMatching);
			}
		}
		actionStateCount = task.keyPath;
		task.keyPath = keyPath;
		hasId ? (keyPath = task.treeContext, task.treeContext = pushTreeContext(keyPath, 1, 0), renderNode(request, task, children, -1), task.treeContext = keyPath) : didEmitActionStateMarkers ? renderNode(request, task, children, -1) : renderNodeDestructive(request, task, children, -1);
		task.keyPath = actionStateCount;
	}
	function renderElement(request, task, keyPath, type, props, ref) {
		if ("function" === typeof type) if (type.prototype && type.prototype.isReactComponent) {
			var newProps = props;
			if ("ref" in props) {
				newProps = {};
				for (var propName in props) "ref" !== propName && (newProps[propName] = props[propName]);
			}
			var defaultProps = type.defaultProps;
			if (defaultProps) {
				newProps === props && (newProps = assign({}, newProps, props));
				for (var propName$44 in defaultProps) void 0 === newProps[propName$44] && (newProps[propName$44] = defaultProps[propName$44]);
			}
			props = newProps;
			newProps = emptyContextObject;
			defaultProps = type.contextType;
			"object" === typeof defaultProps && null !== defaultProps && (newProps = defaultProps._currentValue);
			newProps = new type(props, newProps);
			var initialState = void 0 !== newProps.state ? newProps.state : null;
			newProps.updater = classComponentUpdater;
			newProps.props = props;
			newProps.state = initialState;
			defaultProps = {
				queue: [],
				replace: !1
			};
			newProps._reactInternals = defaultProps;
			ref = type.contextType;
			newProps.context = "object" === typeof ref && null !== ref ? ref._currentValue : emptyContextObject;
			ref = type.getDerivedStateFromProps;
			"function" === typeof ref && (ref = ref(props, initialState), initialState = null === ref || void 0 === ref ? initialState : assign({}, initialState, ref), newProps.state = initialState);
			if ("function" !== typeof type.getDerivedStateFromProps && "function" !== typeof newProps.getSnapshotBeforeUpdate && ("function" === typeof newProps.UNSAFE_componentWillMount || "function" === typeof newProps.componentWillMount)) if (type = newProps.state, "function" === typeof newProps.componentWillMount && newProps.componentWillMount(), "function" === typeof newProps.UNSAFE_componentWillMount && newProps.UNSAFE_componentWillMount(), type !== newProps.state && classComponentUpdater.enqueueReplaceState(newProps, newProps.state, null), null !== defaultProps.queue && 0 < defaultProps.queue.length) if (type = defaultProps.queue, ref = defaultProps.replace, defaultProps.queue = null, defaultProps.replace = !1, ref && 1 === type.length) newProps.state = type[0];
			else {
				defaultProps = ref ? type[0] : newProps.state;
				initialState = !0;
				for (ref = ref ? 1 : 0; ref < type.length; ref++) propName$44 = type[ref], propName$44 = "function" === typeof propName$44 ? propName$44.call(newProps, defaultProps, props, void 0) : propName$44, null != propName$44 && (initialState ? (initialState = !1, defaultProps = assign({}, defaultProps, propName$44)) : assign(defaultProps, propName$44));
				newProps.state = defaultProps;
			}
			else defaultProps.queue = null;
			type = newProps.render();
			if (12 === request.status) throw null;
			props = task.keyPath;
			task.keyPath = keyPath;
			renderNodeDestructive(request, task, type, -1);
			task.keyPath = props;
		} else {
			type = renderWithHooks(request, task, keyPath, type, props, void 0);
			if (12 === request.status) throw null;
			finishFunctionComponent(request, task, keyPath, type, 0 !== localIdCounter, actionStateCounter, actionStateMatchingIndex);
		}
		else if ("string" === typeof type) if (newProps = task.blockedSegment, null === newProps) newProps = props.children, defaultProps = task.formatContext, initialState = task.keyPath, task.formatContext = getChildFormatContext(defaultProps, type, props), task.keyPath = keyPath, renderNode(request, task, newProps, -1), task.formatContext = defaultProps, task.keyPath = initialState;
		else {
			initialState = pushStartInstance(newProps.chunks, type, props, request.resumableState, request.renderState, task.blockedPreamble, task.hoistableState, task.formatContext, newProps.lastPushedText);
			newProps.lastPushedText = !1;
			defaultProps = task.formatContext;
			ref = task.keyPath;
			task.keyPath = keyPath;
			if (3 === (task.formatContext = getChildFormatContext(defaultProps, type, props)).insertionMode) {
				keyPath = createPendingSegment(request, 0, null, task.formatContext, !1, !1);
				newProps.preambleChildren.push(keyPath);
				task.blockedSegment = keyPath;
				try {
					keyPath.status = 6, renderNode(request, task, initialState, -1), keyPath.lastPushedText && keyPath.textEmbedded && keyPath.chunks.push(textSeparator), keyPath.status = 1, finishedSegment(request, task.blockedBoundary, keyPath);
				} finally {
					task.blockedSegment = newProps;
				}
			} else renderNode(request, task, initialState, -1);
			task.formatContext = defaultProps;
			task.keyPath = ref;
			a: {
				task = newProps.chunks;
				request = request.resumableState;
				switch (type) {
					case "title":
					case "style":
					case "script":
					case "area":
					case "base":
					case "br":
					case "col":
					case "embed":
					case "hr":
					case "img":
					case "input":
					case "keygen":
					case "link":
					case "meta":
					case "param":
					case "source":
					case "track":
					case "wbr": break a;
					case "body":
						if (1 >= defaultProps.insertionMode) {
							request.hasBody = !0;
							break a;
						}
						break;
					case "html":
						if (0 === defaultProps.insertionMode) {
							request.hasHtml = !0;
							break a;
						}
						break;
					case "head": if (1 >= defaultProps.insertionMode) break a;
				}
				task.push(endChunkForTag(type));
			}
			newProps.lastPushedText = !1;
		}
		else {
			switch (type) {
				case REACT_LEGACY_HIDDEN_TYPE:
				case REACT_STRICT_MODE_TYPE:
				case REACT_PROFILER_TYPE:
				case REACT_FRAGMENT_TYPE:
					type = task.keyPath;
					task.keyPath = keyPath;
					renderNodeDestructive(request, task, props.children, -1);
					task.keyPath = type;
					return;
				case REACT_ACTIVITY_TYPE:
					type = task.blockedSegment;
					null === type ? "hidden" !== props.mode && (type = task.keyPath, task.keyPath = keyPath, renderNode(request, task, props.children, -1), task.keyPath = type) : "hidden" !== props.mode && (type.chunks.push(startActivityBoundary), type.lastPushedText = !1, newProps = task.keyPath, task.keyPath = keyPath, renderNode(request, task, props.children, -1), task.keyPath = newProps, type.chunks.push(endActivityBoundary), type.lastPushedText = !1);
					return;
				case REACT_SUSPENSE_LIST_TYPE:
					a: {
						type = props.children;
						props = props.revealOrder;
						if ("forwards" === props || "backwards" === props || "unstable_legacy-backwards" === props) {
							if (isArrayImpl(type)) {
								renderSuspenseListRows(request, task, keyPath, type, props);
								break a;
							}
							if (newProps = getIteratorFn(type)) {
								if (newProps = newProps.call(type)) {
									defaultProps = newProps.next();
									if (!defaultProps.done) {
										do
											defaultProps = newProps.next();
										while (!defaultProps.done);
										renderSuspenseListRows(request, task, keyPath, type, props);
									}
									break a;
								}
							}
						}
						"together" === props ? (props = task.keyPath, newProps = task.row, defaultProps = task.row = createSuspenseListRow(null), defaultProps.boundaries = [], defaultProps.together = !0, task.keyPath = keyPath, renderNodeDestructive(request, task, type, -1), 0 === --defaultProps.pendingTasks && finishSuspenseListRow(request, defaultProps), task.keyPath = props, task.row = newProps, null !== newProps && 0 < defaultProps.pendingTasks && (newProps.pendingTasks++, defaultProps.next = newProps)) : (props = task.keyPath, task.keyPath = keyPath, renderNodeDestructive(request, task, type, -1), task.keyPath = props);
					}
					return;
				case REACT_VIEW_TRANSITION_TYPE:
				case REACT_SCOPE_TYPE: throw Error("ReactDOMServer does not yet support scope components.");
				case REACT_SUSPENSE_TYPE:
					a: if (null !== task.replay) {
						type = task.keyPath;
						newProps = task.formatContext;
						defaultProps = task.row;
						task.keyPath = keyPath;
						task.formatContext = getSuspenseContentFormatContext(request.resumableState, newProps);
						task.row = null;
						keyPath = props.children;
						try {
							renderNode(request, task, keyPath, -1);
						} finally {
							task.keyPath = type, task.formatContext = newProps, task.row = defaultProps;
						}
					} else {
						type = task.keyPath;
						ref = task.formatContext;
						var prevRow = task.row;
						propName$44 = task.blockedBoundary;
						propName = task.blockedPreamble;
						var parentHoistableState = task.hoistableState, parentSegment = task.blockedSegment, fallback = props.fallback;
						props = props.children;
						var fallbackAbortSet = /* @__PURE__ */ new Set();
						var newBoundary = 2 > task.formatContext.insertionMode ? createSuspenseBoundary(request, task.row, fallbackAbortSet, createPreambleState(), createPreambleState()) : createSuspenseBoundary(request, task.row, fallbackAbortSet, null, null);
						null !== request.trackedPostpones && (newBoundary.trackedContentKeyPath = keyPath);
						var boundarySegment = createPendingSegment(request, parentSegment.chunks.length, newBoundary, task.formatContext, !1, !1);
						parentSegment.children.push(boundarySegment);
						parentSegment.lastPushedText = !1;
						var contentRootSegment = createPendingSegment(request, 0, null, task.formatContext, !1, !1);
						contentRootSegment.parentFlushed = !0;
						if (null !== request.trackedPostpones) {
							newProps = task.componentStack;
							defaultProps = [
								keyPath[0],
								"Suspense Fallback",
								keyPath[2]
							];
							initialState = [
								defaultProps[1],
								defaultProps[2],
								[],
								null
							];
							request.trackedPostpones.workingMap.set(defaultProps, initialState);
							newBoundary.trackedFallbackNode = initialState;
							task.blockedSegment = boundarySegment;
							task.blockedPreamble = newBoundary.fallbackPreamble;
							task.keyPath = defaultProps;
							task.formatContext = getSuspenseFallbackFormatContext(request.resumableState, ref);
							task.componentStack = replaceSuspenseComponentStackWithSuspenseFallbackStack(newProps);
							boundarySegment.status = 6;
							try {
								renderNode(request, task, fallback, -1), boundarySegment.lastPushedText && boundarySegment.textEmbedded && boundarySegment.chunks.push(textSeparator), boundarySegment.status = 1, finishedSegment(request, propName$44, boundarySegment);
							} catch (thrownValue) {
								throw boundarySegment.status = 12 === request.status ? 3 : 4, thrownValue;
							} finally {
								task.blockedSegment = parentSegment, task.blockedPreamble = propName, task.keyPath = type, task.formatContext = ref;
							}
							task = createRenderTask(request, null, props, -1, newBoundary, contentRootSegment, newBoundary.contentPreamble, newBoundary.contentState, task.abortSet, keyPath, getSuspenseContentFormatContext(request.resumableState, task.formatContext), task.context, task.treeContext, null, newProps);
							pushComponentStack(task);
							request.pingedTasks.push(task);
						} else {
							task.blockedBoundary = newBoundary;
							task.blockedPreamble = newBoundary.contentPreamble;
							task.hoistableState = newBoundary.contentState;
							task.blockedSegment = contentRootSegment;
							task.keyPath = keyPath;
							task.formatContext = getSuspenseContentFormatContext(request.resumableState, ref);
							task.row = null;
							contentRootSegment.status = 6;
							try {
								if (renderNode(request, task, props, -1), contentRootSegment.lastPushedText && contentRootSegment.textEmbedded && contentRootSegment.chunks.push(textSeparator), contentRootSegment.status = 1, finishedSegment(request, newBoundary, contentRootSegment), queueCompletedSegment(newBoundary, contentRootSegment), 0 === newBoundary.pendingTasks && 0 === newBoundary.status) {
									if (newBoundary.status = 1, !isEligibleForOutlining(request, newBoundary)) {
										null !== prevRow && 0 === --prevRow.pendingTasks && finishSuspenseListRow(request, prevRow);
										0 === request.pendingRootTasks && task.blockedPreamble && preparePreamble(request);
										break a;
									}
								} else null !== prevRow && prevRow.together && tryToResolveTogetherRow(request, prevRow);
							} catch (thrownValue$31) {
								newBoundary.status = 4, 12 === request.status ? (contentRootSegment.status = 3, newProps = request.fatalError) : (contentRootSegment.status = 4, newProps = thrownValue$31), defaultProps = getThrownInfo(task.componentStack), initialState = logRecoverableError(request, newProps, defaultProps), newBoundary.errorDigest = initialState, untrackBoundary(request, newBoundary);
							} finally {
								task.blockedBoundary = propName$44, task.blockedPreamble = propName, task.hoistableState = parentHoistableState, task.blockedSegment = parentSegment, task.keyPath = type, task.formatContext = ref, task.row = prevRow;
							}
							task = createRenderTask(request, null, fallback, -1, propName$44, boundarySegment, newBoundary.fallbackPreamble, newBoundary.fallbackState, fallbackAbortSet, [
								keyPath[0],
								"Suspense Fallback",
								keyPath[2]
							], getSuspenseFallbackFormatContext(request.resumableState, task.formatContext), task.context, task.treeContext, task.row, replaceSuspenseComponentStackWithSuspenseFallbackStack(task.componentStack));
							pushComponentStack(task);
							request.pingedTasks.push(task);
						}
					}
					return;
			}
			if ("object" === typeof type && null !== type) switch (type.$$typeof) {
				case REACT_FORWARD_REF_TYPE:
					if ("ref" in props) for (parentSegment in newProps = {}, props) "ref" !== parentSegment && (newProps[parentSegment] = props[parentSegment]);
					else newProps = props;
					type = renderWithHooks(request, task, keyPath, type.render, newProps, ref);
					finishFunctionComponent(request, task, keyPath, type, 0 !== localIdCounter, actionStateCounter, actionStateMatchingIndex);
					return;
				case REACT_MEMO_TYPE:
					renderElement(request, task, keyPath, type.type, props, ref);
					return;
				case REACT_CONTEXT_TYPE:
					defaultProps = props.children;
					newProps = task.keyPath;
					props = props.value;
					initialState = type._currentValue;
					type._currentValue = props;
					ref = currentActiveSnapshot;
					currentActiveSnapshot = type = {
						parent: ref,
						depth: null === ref ? 0 : ref.depth + 1,
						context: type,
						parentValue: initialState,
						value: props
					};
					task.context = type;
					task.keyPath = keyPath;
					renderNodeDestructive(request, task, defaultProps, -1);
					request = currentActiveSnapshot;
					if (null === request) throw Error("Tried to pop a Context at the root of the app. This is a bug in React.");
					request.context._currentValue = request.parentValue;
					request = currentActiveSnapshot = request.parent;
					task.context = request;
					task.keyPath = newProps;
					return;
				case REACT_CONSUMER_TYPE:
					props = props.children;
					type = props(type._context._currentValue);
					props = task.keyPath;
					task.keyPath = keyPath;
					renderNodeDestructive(request, task, type, -1);
					task.keyPath = props;
					return;
				case REACT_LAZY_TYPE:
					newProps = type._init;
					type = newProps(type._payload);
					if (12 === request.status) throw null;
					renderElement(request, task, keyPath, type, props, ref);
					return;
			}
			throw Error("Element type is invalid: expected a string (for built-in components) or a class/function (for composite components) but got: " + ((null == type ? type : typeof type) + "."));
		}
	}
	function resumeNode(request, task, segmentId, node, childIndex) {
		var prevReplay = task.replay, blockedBoundary = task.blockedBoundary, resumedSegment = createPendingSegment(request, 0, null, task.formatContext, !1, !1);
		resumedSegment.id = segmentId;
		resumedSegment.parentFlushed = !0;
		try {
			task.replay = null, task.blockedSegment = resumedSegment, renderNode(request, task, node, childIndex), resumedSegment.status = 1, finishedSegment(request, blockedBoundary, resumedSegment), null === blockedBoundary ? request.completedRootSegment = resumedSegment : (queueCompletedSegment(blockedBoundary, resumedSegment), blockedBoundary.parentFlushed && request.partialBoundaries.push(blockedBoundary));
		} finally {
			task.replay = prevReplay, task.blockedSegment = null;
		}
	}
	function renderNodeDestructive(request, task, node, childIndex) {
		null !== task.replay && "number" === typeof task.replay.slots ? resumeNode(request, task, task.replay.slots, node, childIndex) : (task.node = node, task.childIndex = childIndex, node = task.componentStack, pushComponentStack(task), retryNode(request, task), task.componentStack = node);
	}
	function retryNode(request, task) {
		var node = task.node, childIndex = task.childIndex;
		if (null !== node) {
			if ("object" === typeof node) {
				switch (node.$$typeof) {
					case REACT_ELEMENT_TYPE:
						var type = node.type, key = node.key, props = node.props;
						node = props.ref;
						var ref = void 0 !== node ? node : null, name = getComponentNameFromType(type), keyOrIndex = null == key ? -1 === childIndex ? 0 : childIndex : key;
						key = [
							task.keyPath,
							name,
							keyOrIndex
						];
						if (null !== task.replay) a: {
							var replay = task.replay;
							childIndex = replay.nodes;
							for (node = 0; node < childIndex.length; node++) {
								var node$jscomp$0 = childIndex[node];
								if (keyOrIndex === node$jscomp$0[1]) {
									if (4 === node$jscomp$0.length) {
										if (null !== name && name !== node$jscomp$0[0]) throw Error("Expected the resume to render <" + node$jscomp$0[0] + "> in this slot but instead it rendered <" + name + ">. The tree doesn't match so React will fallback to client rendering.");
										var childNodes = node$jscomp$0[2];
										name = node$jscomp$0[3];
										keyOrIndex = task.node;
										task.replay = {
											nodes: childNodes,
											slots: name,
											pendingTasks: 1
										};
										try {
											renderElement(request, task, key, type, props, ref);
											if (1 === task.replay.pendingTasks && 0 < task.replay.nodes.length) throw Error("Couldn't find all resumable slots by key/index during replaying. The tree doesn't match so React will fallback to client rendering.");
											task.replay.pendingTasks--;
										} catch (x) {
											if ("object" === typeof x && null !== x && (x === SuspenseException || "function" === typeof x.then)) throw task.node === keyOrIndex ? task.replay = replay : childIndex.splice(node, 1), x;
											task.replay.pendingTasks--;
											props = getThrownInfo(task.componentStack);
											key = request;
											request = task.blockedBoundary;
											type = x;
											props = logRecoverableError(key, type, props);
											abortRemainingReplayNodes(key, request, childNodes, name, type, props);
										}
										task.replay = replay;
									} else {
										if (type !== REACT_SUSPENSE_TYPE) throw Error("Expected the resume to render <Suspense> in this slot but instead it rendered <" + (getComponentNameFromType(type) || "Unknown") + ">. The tree doesn't match so React will fallback to client rendering.");
										b: {
											replay = void 0;
											type = node$jscomp$0[5];
											ref = node$jscomp$0[2];
											name = node$jscomp$0[3];
											keyOrIndex = null === node$jscomp$0[4] ? [] : node$jscomp$0[4][2];
											node$jscomp$0 = null === node$jscomp$0[4] ? null : node$jscomp$0[4][3];
											var prevKeyPath = task.keyPath, prevContext = task.formatContext, prevRow = task.row, previousReplaySet = task.replay, parentBoundary = task.blockedBoundary, parentHoistableState = task.hoistableState, content = props.children, fallback = props.fallback, fallbackAbortSet = /* @__PURE__ */ new Set();
											props = 2 > task.formatContext.insertionMode ? createSuspenseBoundary(request, task.row, fallbackAbortSet, createPreambleState(), createPreambleState()) : createSuspenseBoundary(request, task.row, fallbackAbortSet, null, null);
											props.parentFlushed = !0;
											props.rootSegmentID = type;
											task.blockedBoundary = props;
											task.hoistableState = props.contentState;
											task.keyPath = key;
											task.formatContext = getSuspenseContentFormatContext(request.resumableState, prevContext);
											task.row = null;
											task.replay = {
												nodes: ref,
												slots: name,
												pendingTasks: 1
											};
											try {
												renderNode(request, task, content, -1);
												if (1 === task.replay.pendingTasks && 0 < task.replay.nodes.length) throw Error("Couldn't find all resumable slots by key/index during replaying. The tree doesn't match so React will fallback to client rendering.");
												task.replay.pendingTasks--;
												if (0 === props.pendingTasks && 0 === props.status) {
													props.status = 1;
													request.completedBoundaries.push(props);
													break b;
												}
											} catch (error) {
												props.status = 4, childNodes = getThrownInfo(task.componentStack), replay = logRecoverableError(request, error, childNodes), props.errorDigest = replay, task.replay.pendingTasks--, request.clientRenderedBoundaries.push(props);
											} finally {
												task.blockedBoundary = parentBoundary, task.hoistableState = parentHoistableState, task.replay = previousReplaySet, task.keyPath = prevKeyPath, task.formatContext = prevContext, task.row = prevRow;
											}
											childNodes = createReplayTask(request, null, {
												nodes: keyOrIndex,
												slots: node$jscomp$0,
												pendingTasks: 0
											}, fallback, -1, parentBoundary, props.fallbackState, fallbackAbortSet, [
												key[0],
												"Suspense Fallback",
												key[2]
											], getSuspenseFallbackFormatContext(request.resumableState, task.formatContext), task.context, task.treeContext, task.row, replaceSuspenseComponentStackWithSuspenseFallbackStack(task.componentStack));
											pushComponentStack(childNodes);
											request.pingedTasks.push(childNodes);
										}
									}
									childIndex.splice(node, 1);
									break a;
								}
							}
						}
						else renderElement(request, task, key, type, props, ref);
						return;
					case REACT_PORTAL_TYPE: throw Error("Portals are not currently supported by the server renderer. Render them conditionally so that they only appear on the client render.");
					case REACT_LAZY_TYPE:
						childNodes = node._init;
						node = childNodes(node._payload);
						if (12 === request.status) throw null;
						renderNodeDestructive(request, task, node, childIndex);
						return;
				}
				if (isArrayImpl(node)) {
					renderChildrenArray(request, task, node, childIndex);
					return;
				}
				if (childNodes = getIteratorFn(node)) {
					if (childNodes = childNodes.call(node)) {
						node = childNodes.next();
						if (!node.done) {
							props = [];
							do
								props.push(node.value), node = childNodes.next();
							while (!node.done);
							renderChildrenArray(request, task, props, childIndex);
						}
						return;
					}
				}
				if ("function" === typeof node.then) return task.thenableState = null, renderNodeDestructive(request, task, unwrapThenable(node), childIndex);
				if (node.$$typeof === REACT_CONTEXT_TYPE) return renderNodeDestructive(request, task, node._currentValue, childIndex);
				childIndex = Object.prototype.toString.call(node);
				throw Error("Objects are not valid as a React child (found: " + ("[object Object]" === childIndex ? "object with keys {" + Object.keys(node).join(", ") + "}" : childIndex) + "). If you meant to render a collection of children, use an array instead.");
			}
			if ("string" === typeof node) childIndex = task.blockedSegment, null !== childIndex && (childIndex.lastPushedText = pushTextInstance(childIndex.chunks, node, request.renderState, childIndex.lastPushedText));
			else if ("number" === typeof node || "bigint" === typeof node) childIndex = task.blockedSegment, null !== childIndex && (childIndex.lastPushedText = pushTextInstance(childIndex.chunks, "" + node, request.renderState, childIndex.lastPushedText));
		}
	}
	function renderChildrenArray(request, task, children, childIndex) {
		var prevKeyPath = task.keyPath;
		if (-1 !== childIndex && (task.keyPath = [
			task.keyPath,
			"Fragment",
			childIndex
		], null !== task.replay)) {
			for (var replay = task.replay, replayNodes = replay.nodes, j = 0; j < replayNodes.length; j++) {
				var node = replayNodes[j];
				if (node[1] === childIndex) {
					childIndex = node[2];
					node = node[3];
					task.replay = {
						nodes: childIndex,
						slots: node,
						pendingTasks: 1
					};
					try {
						renderChildrenArray(request, task, children, -1);
						if (1 === task.replay.pendingTasks && 0 < task.replay.nodes.length) throw Error("Couldn't find all resumable slots by key/index during replaying. The tree doesn't match so React will fallback to client rendering.");
						task.replay.pendingTasks--;
					} catch (x) {
						if ("object" === typeof x && null !== x && (x === SuspenseException || "function" === typeof x.then)) throw x;
						task.replay.pendingTasks--;
						children = getThrownInfo(task.componentStack);
						var boundary = task.blockedBoundary, error = x;
						children = logRecoverableError(request, error, children);
						abortRemainingReplayNodes(request, boundary, childIndex, node, error, children);
					}
					task.replay = replay;
					replayNodes.splice(j, 1);
					break;
				}
			}
			task.keyPath = prevKeyPath;
			return;
		}
		replay = task.treeContext;
		replayNodes = children.length;
		if (null !== task.replay && (j = task.replay.slots, null !== j && "object" === typeof j)) {
			for (childIndex = 0; childIndex < replayNodes; childIndex++) node = children[childIndex], task.treeContext = pushTreeContext(replay, replayNodes, childIndex), boundary = j[childIndex], "number" === typeof boundary ? (resumeNode(request, task, boundary, node, childIndex), delete j[childIndex]) : renderNode(request, task, node, childIndex);
			task.treeContext = replay;
			task.keyPath = prevKeyPath;
			return;
		}
		for (j = 0; j < replayNodes; j++) childIndex = children[j], task.treeContext = pushTreeContext(replay, replayNodes, j), renderNode(request, task, childIndex, j);
		task.treeContext = replay;
		task.keyPath = prevKeyPath;
	}
	function trackPostponedBoundary(request, trackedPostpones, boundary) {
		boundary.status = 5;
		boundary.rootSegmentID = request.nextSegmentId++;
		request = boundary.trackedContentKeyPath;
		if (null === request) throw Error("It should not be possible to postpone at the root. This is a bug in React.");
		var fallbackReplayNode = boundary.trackedFallbackNode, children = [], boundaryNode = trackedPostpones.workingMap.get(request);
		if (void 0 === boundaryNode) return boundary = [
			request[1],
			request[2],
			children,
			null,
			fallbackReplayNode,
			boundary.rootSegmentID
		], trackedPostpones.workingMap.set(request, boundary), addToReplayParent(boundary, request[0], trackedPostpones), boundary;
		boundaryNode[4] = fallbackReplayNode;
		boundaryNode[5] = boundary.rootSegmentID;
		return boundaryNode;
	}
	function trackPostpone(request, trackedPostpones, task, segment) {
		segment.status = 5;
		var keyPath = task.keyPath, boundary = task.blockedBoundary;
		if (null === boundary) segment.id = request.nextSegmentId++, trackedPostpones.rootSlots = segment.id, null !== request.completedRootSegment && (request.completedRootSegment.status = 5);
		else {
			if (null !== boundary && 0 === boundary.status) {
				var boundaryNode = trackPostponedBoundary(request, trackedPostpones, boundary);
				if (boundary.trackedContentKeyPath === keyPath && -1 === task.childIndex) {
					-1 === segment.id && (segment.id = segment.parentFlushed ? boundary.rootSegmentID : request.nextSegmentId++);
					boundaryNode[3] = segment.id;
					return;
				}
			}
			-1 === segment.id && (segment.id = segment.parentFlushed && null !== boundary ? boundary.rootSegmentID : request.nextSegmentId++);
			if (-1 === task.childIndex) null === keyPath ? trackedPostpones.rootSlots = segment.id : (task = trackedPostpones.workingMap.get(keyPath), void 0 === task ? (task = [
				keyPath[1],
				keyPath[2],
				[],
				segment.id
			], addToReplayParent(task, keyPath[0], trackedPostpones)) : task[3] = segment.id);
			else {
				if (null === keyPath) {
					if (request = trackedPostpones.rootSlots, null === request) request = trackedPostpones.rootSlots = {};
					else if ("number" === typeof request) throw Error("It should not be possible to postpone both at the root of an element as well as a slot below. This is a bug in React.");
				} else if (boundary = trackedPostpones.workingMap, boundaryNode = boundary.get(keyPath), void 0 === boundaryNode) request = {}, boundaryNode = [
					keyPath[1],
					keyPath[2],
					[],
					request
				], boundary.set(keyPath, boundaryNode), addToReplayParent(boundaryNode, keyPath[0], trackedPostpones);
				else if (request = boundaryNode[3], null === request) request = boundaryNode[3] = {};
				else if ("number" === typeof request) throw Error("It should not be possible to postpone both at the root of an element as well as a slot below. This is a bug in React.");
				request[task.childIndex] = segment.id;
			}
		}
	}
	function untrackBoundary(request, boundary) {
		request = request.trackedPostpones;
		null !== request && (boundary = boundary.trackedContentKeyPath, null !== boundary && (boundary = request.workingMap.get(boundary), void 0 !== boundary && (boundary.length = 4, boundary[2] = [], boundary[3] = null)));
	}
	function spawnNewSuspendedReplayTask(request, task, thenableState) {
		return createReplayTask(request, thenableState, task.replay, task.node, task.childIndex, task.blockedBoundary, task.hoistableState, task.abortSet, task.keyPath, task.formatContext, task.context, task.treeContext, task.row, task.componentStack);
	}
	function spawnNewSuspendedRenderTask(request, task, thenableState) {
		var segment = task.blockedSegment, newSegment = createPendingSegment(request, segment.chunks.length, null, task.formatContext, segment.lastPushedText, !0);
		segment.children.push(newSegment);
		segment.lastPushedText = !1;
		return createRenderTask(request, thenableState, task.node, task.childIndex, task.blockedBoundary, newSegment, task.blockedPreamble, task.hoistableState, task.abortSet, task.keyPath, task.formatContext, task.context, task.treeContext, task.row, task.componentStack);
	}
	function renderNode(request, task, node, childIndex) {
		var previousFormatContext = task.formatContext, previousContext = task.context, previousKeyPath = task.keyPath, previousTreeContext = task.treeContext, previousComponentStack = task.componentStack, segment = task.blockedSegment;
		if (null === segment) {
			segment = task.replay;
			try {
				return renderNodeDestructive(request, task, node, childIndex);
			} catch (thrownValue) {
				if (resetHooksState(), node = thrownValue === SuspenseException ? getSuspendedThenable() : thrownValue, 12 !== request.status && "object" === typeof node && null !== node) {
					if ("function" === typeof node.then) {
						childIndex = thrownValue === SuspenseException ? getThenableStateAfterSuspending() : null;
						request = spawnNewSuspendedReplayTask(request, task, childIndex).ping;
						node.then(request, request);
						task.formatContext = previousFormatContext;
						task.context = previousContext;
						task.keyPath = previousKeyPath;
						task.treeContext = previousTreeContext;
						task.componentStack = previousComponentStack;
						task.replay = segment;
						switchContext(previousContext);
						return;
					}
					if ("Maximum call stack size exceeded" === node.message) {
						node = thrownValue === SuspenseException ? getThenableStateAfterSuspending() : null;
						node = spawnNewSuspendedReplayTask(request, task, node);
						request.pingedTasks.push(node);
						task.formatContext = previousFormatContext;
						task.context = previousContext;
						task.keyPath = previousKeyPath;
						task.treeContext = previousTreeContext;
						task.componentStack = previousComponentStack;
						task.replay = segment;
						switchContext(previousContext);
						return;
					}
				}
			}
		} else {
			var childrenLength = segment.children.length, chunkLength = segment.chunks.length;
			try {
				return renderNodeDestructive(request, task, node, childIndex);
			} catch (thrownValue$63) {
				if (resetHooksState(), segment.children.length = childrenLength, segment.chunks.length = chunkLength, node = thrownValue$63 === SuspenseException ? getSuspendedThenable() : thrownValue$63, 12 !== request.status && "object" === typeof node && null !== node) {
					if ("function" === typeof node.then) {
						segment = node;
						node = thrownValue$63 === SuspenseException ? getThenableStateAfterSuspending() : null;
						request = spawnNewSuspendedRenderTask(request, task, node).ping;
						segment.then(request, request);
						task.formatContext = previousFormatContext;
						task.context = previousContext;
						task.keyPath = previousKeyPath;
						task.treeContext = previousTreeContext;
						task.componentStack = previousComponentStack;
						switchContext(previousContext);
						return;
					}
					if ("Maximum call stack size exceeded" === node.message) {
						segment = thrownValue$63 === SuspenseException ? getThenableStateAfterSuspending() : null;
						segment = spawnNewSuspendedRenderTask(request, task, segment);
						request.pingedTasks.push(segment);
						task.formatContext = previousFormatContext;
						task.context = previousContext;
						task.keyPath = previousKeyPath;
						task.treeContext = previousTreeContext;
						task.componentStack = previousComponentStack;
						switchContext(previousContext);
						return;
					}
				}
			}
		}
		task.formatContext = previousFormatContext;
		task.context = previousContext;
		task.keyPath = previousKeyPath;
		task.treeContext = previousTreeContext;
		switchContext(previousContext);
		throw node;
	}
	function abortTaskSoft(task) {
		var boundary = task.blockedBoundary, segment = task.blockedSegment;
		null !== segment && (segment.status = 3, finishedTask(this, boundary, task.row, segment));
	}
	function abortRemainingReplayNodes(request$jscomp$0, boundary, nodes, slots, error, errorDigest$jscomp$0) {
		for (var i = 0; i < nodes.length; i++) {
			var node = nodes[i];
			if (4 === node.length) abortRemainingReplayNodes(request$jscomp$0, boundary, node[2], node[3], error, errorDigest$jscomp$0);
			else {
				node = node[5];
				var request = request$jscomp$0, errorDigest = errorDigest$jscomp$0, resumedBoundary = createSuspenseBoundary(request, null, /* @__PURE__ */ new Set(), null, null);
				resumedBoundary.parentFlushed = !0;
				resumedBoundary.rootSegmentID = node;
				resumedBoundary.status = 4;
				resumedBoundary.errorDigest = errorDigest;
				resumedBoundary.parentFlushed && request.clientRenderedBoundaries.push(resumedBoundary);
			}
		}
		nodes.length = 0;
		if (null !== slots) {
			if (null === boundary) throw Error("We should not have any resumable nodes in the shell. This is a bug in React.");
			4 !== boundary.status && (boundary.status = 4, boundary.errorDigest = errorDigest$jscomp$0, boundary.parentFlushed && request$jscomp$0.clientRenderedBoundaries.push(boundary));
			if ("object" === typeof slots) for (var index in slots) delete slots[index];
		}
	}
	function abortTask(task, request, error) {
		var boundary = task.blockedBoundary, segment = task.blockedSegment;
		if (null !== segment) {
			if (6 === segment.status) return;
			segment.status = 3;
		}
		var errorInfo = getThrownInfo(task.componentStack);
		if (null === boundary) {
			if (13 !== request.status && 14 !== request.status) {
				boundary = task.replay;
				if (null === boundary) {
					null !== request.trackedPostpones && null !== segment ? (boundary = request.trackedPostpones, logRecoverableError(request, error, errorInfo), trackPostpone(request, boundary, task, segment), finishedTask(request, null, task.row, segment)) : (logRecoverableError(request, error, errorInfo), fatalError(request, error));
					return;
				}
				boundary.pendingTasks--;
				0 === boundary.pendingTasks && 0 < boundary.nodes.length && (segment = logRecoverableError(request, error, errorInfo), abortRemainingReplayNodes(request, null, boundary.nodes, boundary.slots, error, segment));
				request.pendingRootTasks--;
				0 === request.pendingRootTasks && completeShell(request);
			}
		} else {
			var trackedPostpones$64 = request.trackedPostpones;
			if (4 !== boundary.status) {
				if (null !== trackedPostpones$64 && null !== segment) return logRecoverableError(request, error, errorInfo), trackPostpone(request, trackedPostpones$64, task, segment), boundary.fallbackAbortableTasks.forEach(function(fallbackTask) {
					return abortTask(fallbackTask, request, error);
				}), boundary.fallbackAbortableTasks.clear(), finishedTask(request, boundary, task.row, segment);
				boundary.status = 4;
				segment = logRecoverableError(request, error, errorInfo);
				boundary.status = 4;
				boundary.errorDigest = segment;
				untrackBoundary(request, boundary);
				boundary.parentFlushed && request.clientRenderedBoundaries.push(boundary);
			}
			boundary.pendingTasks--;
			segment = boundary.row;
			null !== segment && 0 === --segment.pendingTasks && finishSuspenseListRow(request, segment);
			boundary.fallbackAbortableTasks.forEach(function(fallbackTask) {
				return abortTask(fallbackTask, request, error);
			});
			boundary.fallbackAbortableTasks.clear();
		}
		task = task.row;
		null !== task && 0 === --task.pendingTasks && finishSuspenseListRow(request, task);
		request.allPendingTasks--;
		0 === request.allPendingTasks && completeAll(request);
	}
	function safelyEmitEarlyPreloads(request, shellComplete) {
		try {
			var renderState = request.renderState, onHeaders = renderState.onHeaders;
			if (onHeaders) {
				var headers = renderState.headers;
				if (headers) {
					renderState.headers = null;
					var linkHeader = headers.preconnects;
					headers.fontPreloads && (linkHeader && (linkHeader += ", "), linkHeader += headers.fontPreloads);
					headers.highImagePreloads && (linkHeader && (linkHeader += ", "), linkHeader += headers.highImagePreloads);
					if (!shellComplete) {
						var queueIter = renderState.styles.values(), queueStep = queueIter.next();
						b: for (; 0 < headers.remainingCapacity && !queueStep.done; queueStep = queueIter.next()) for (var sheetIter = queueStep.value.sheets.values(), sheetStep = sheetIter.next(); 0 < headers.remainingCapacity && !sheetStep.done; sheetStep = sheetIter.next()) {
							var sheet = sheetStep.value, props = sheet.props, key = props.href, props$jscomp$0 = sheet.props, header = getPreloadAsHeader(props$jscomp$0.href, "style", {
								crossOrigin: props$jscomp$0.crossOrigin,
								integrity: props$jscomp$0.integrity,
								nonce: props$jscomp$0.nonce,
								type: props$jscomp$0.type,
								fetchPriority: props$jscomp$0.fetchPriority,
								referrerPolicy: props$jscomp$0.referrerPolicy,
								media: props$jscomp$0.media
							});
							if (0 <= (headers.remainingCapacity -= header.length + 2)) renderState.resets.style[key] = PRELOAD_NO_CREDS, linkHeader && (linkHeader += ", "), linkHeader += header, renderState.resets.style[key] = "string" === typeof props.crossOrigin || "string" === typeof props.integrity ? [props.crossOrigin, props.integrity] : PRELOAD_NO_CREDS;
							else break b;
						}
					}
					linkHeader ? onHeaders({ Link: linkHeader }) : onHeaders({});
				}
			}
		} catch (error) {
			logRecoverableError(request, error, {});
		}
	}
	function completeShell(request) {
		null === request.trackedPostpones && safelyEmitEarlyPreloads(request, !0);
		null === request.trackedPostpones && preparePreamble(request);
		request.onShellError = noop;
		request = request.onShellReady;
		request();
	}
	function completeAll(request) {
		safelyEmitEarlyPreloads(request, null === request.trackedPostpones ? !0 : null === request.completedRootSegment || 5 !== request.completedRootSegment.status);
		preparePreamble(request);
		request = request.onAllReady;
		request();
	}
	function queueCompletedSegment(boundary, segment) {
		if (0 === segment.chunks.length && 1 === segment.children.length && null === segment.children[0].boundary && -1 === segment.children[0].id) {
			var childSegment = segment.children[0];
			childSegment.id = segment.id;
			childSegment.parentFlushed = !0;
			1 !== childSegment.status && 3 !== childSegment.status && 4 !== childSegment.status || queueCompletedSegment(boundary, childSegment);
		} else boundary.completedSegments.push(segment);
	}
	function finishedSegment(request, boundary, segment) {
		if (null !== byteLengthOfChunk) {
			segment = segment.chunks;
			for (var segmentByteSize = 0, i = 0; i < segment.length; i++) segmentByteSize += segment[i].byteLength;
			null === boundary ? request.byteSize += segmentByteSize : boundary.byteSize += segmentByteSize;
		}
	}
	function finishedTask(request, boundary, row, segment) {
		null !== row && (0 === --row.pendingTasks ? finishSuspenseListRow(request, row) : row.together && tryToResolveTogetherRow(request, row));
		request.allPendingTasks--;
		if (null === boundary) {
			if (null !== segment && segment.parentFlushed) {
				if (null !== request.completedRootSegment) throw Error("There can only be one root segment. This is a bug in React.");
				request.completedRootSegment = segment;
			}
			request.pendingRootTasks--;
			0 === request.pendingRootTasks && completeShell(request);
		} else if (boundary.pendingTasks--, 4 !== boundary.status) if (0 === boundary.pendingTasks) {
			if (0 === boundary.status && (boundary.status = 1), null !== segment && segment.parentFlushed && (1 === segment.status || 3 === segment.status) && queueCompletedSegment(boundary, segment), boundary.parentFlushed && request.completedBoundaries.push(boundary), 1 === boundary.status) row = boundary.row, null !== row && hoistHoistables(row.hoistables, boundary.contentState), isEligibleForOutlining(request, boundary) || (boundary.fallbackAbortableTasks.forEach(abortTaskSoft, request), boundary.fallbackAbortableTasks.clear(), null !== row && 0 === --row.pendingTasks && finishSuspenseListRow(request, row)), 0 === request.pendingRootTasks && null === request.trackedPostpones && null !== boundary.contentPreamble && preparePreamble(request);
			else if (5 === boundary.status && (boundary = boundary.row, null !== boundary)) {
				if (null !== request.trackedPostpones) {
					row = request.trackedPostpones;
					var postponedRow = boundary.next;
					if (null !== postponedRow && (segment = postponedRow.boundaries, null !== segment)) for (postponedRow.boundaries = null, postponedRow = 0; postponedRow < segment.length; postponedRow++) {
						var postponedBoundary = segment[postponedRow];
						trackPostponedBoundary(request, row, postponedBoundary);
						finishedTask(request, postponedBoundary, null, null);
					}
				}
				0 === --boundary.pendingTasks && finishSuspenseListRow(request, boundary);
			}
		} else null === segment || !segment.parentFlushed || 1 !== segment.status && 3 !== segment.status || (queueCompletedSegment(boundary, segment), 1 === boundary.completedSegments.length && boundary.parentFlushed && request.partialBoundaries.push(boundary)), boundary = boundary.row, null !== boundary && boundary.together && tryToResolveTogetherRow(request, boundary);
		0 === request.allPendingTasks && completeAll(request);
	}
	function performWork(request$jscomp$2) {
		if (14 !== request$jscomp$2.status && 13 !== request$jscomp$2.status) {
			var prevContext = currentActiveSnapshot, prevDispatcher = ReactSharedInternals.H;
			ReactSharedInternals.H = HooksDispatcher;
			var prevAsyncDispatcher = ReactSharedInternals.A;
			ReactSharedInternals.A = DefaultAsyncDispatcher;
			var prevRequest = currentRequest;
			currentRequest = request$jscomp$2;
			var prevResumableState = currentResumableState;
			currentResumableState = request$jscomp$2.resumableState;
			try {
				var pingedTasks = request$jscomp$2.pingedTasks, i;
				for (i = 0; i < pingedTasks.length; i++) {
					var task = pingedTasks[i], request = request$jscomp$2, segment = task.blockedSegment;
					if (null === segment) {
						var request$jscomp$0 = request;
						if (0 !== task.replay.pendingTasks) {
							switchContext(task.context);
							try {
								"number" === typeof task.replay.slots ? resumeNode(request$jscomp$0, task, task.replay.slots, task.node, task.childIndex) : retryNode(request$jscomp$0, task);
								if (1 === task.replay.pendingTasks && 0 < task.replay.nodes.length) throw Error("Couldn't find all resumable slots by key/index during replaying. The tree doesn't match so React will fallback to client rendering.");
								task.replay.pendingTasks--;
								task.abortSet.delete(task);
								finishedTask(request$jscomp$0, task.blockedBoundary, task.row, null);
							} catch (thrownValue) {
								resetHooksState();
								var x = thrownValue === SuspenseException ? getSuspendedThenable() : thrownValue;
								if ("object" === typeof x && null !== x && "function" === typeof x.then) {
									var ping = task.ping;
									x.then(ping, ping);
									task.thenableState = thrownValue === SuspenseException ? getThenableStateAfterSuspending() : null;
								} else {
									task.replay.pendingTasks--;
									task.abortSet.delete(task);
									var errorInfo = getThrownInfo(task.componentStack);
									request = void 0;
									var request$jscomp$1 = request$jscomp$0, boundary = task.blockedBoundary, error$jscomp$0 = 12 === request$jscomp$0.status ? request$jscomp$0.fatalError : x, replayNodes = task.replay.nodes, resumeSlots = task.replay.slots;
									request = logRecoverableError(request$jscomp$1, error$jscomp$0, errorInfo);
									abortRemainingReplayNodes(request$jscomp$1, boundary, replayNodes, resumeSlots, error$jscomp$0, request);
									request$jscomp$0.pendingRootTasks--;
									0 === request$jscomp$0.pendingRootTasks && completeShell(request$jscomp$0);
									request$jscomp$0.allPendingTasks--;
									0 === request$jscomp$0.allPendingTasks && completeAll(request$jscomp$0);
								}
							}
						}
					} else if (request$jscomp$0 = void 0, request$jscomp$1 = segment, 0 === request$jscomp$1.status) {
						request$jscomp$1.status = 6;
						switchContext(task.context);
						var childrenLength = request$jscomp$1.children.length, chunkLength = request$jscomp$1.chunks.length;
						try {
							retryNode(request, task), request$jscomp$1.lastPushedText && request$jscomp$1.textEmbedded && request$jscomp$1.chunks.push(textSeparator), task.abortSet.delete(task), request$jscomp$1.status = 1, finishedSegment(request, task.blockedBoundary, request$jscomp$1), finishedTask(request, task.blockedBoundary, task.row, request$jscomp$1);
						} catch (thrownValue) {
							resetHooksState();
							request$jscomp$1.children.length = childrenLength;
							request$jscomp$1.chunks.length = chunkLength;
							var x$jscomp$0 = thrownValue === SuspenseException ? getSuspendedThenable() : 12 === request.status ? request.fatalError : thrownValue;
							if (12 === request.status && null !== request.trackedPostpones) {
								var trackedPostpones = request.trackedPostpones, thrownInfo = getThrownInfo(task.componentStack);
								task.abortSet.delete(task);
								logRecoverableError(request, x$jscomp$0, thrownInfo);
								trackPostpone(request, trackedPostpones, task, request$jscomp$1);
								finishedTask(request, task.blockedBoundary, task.row, request$jscomp$1);
							} else if ("object" === typeof x$jscomp$0 && null !== x$jscomp$0 && "function" === typeof x$jscomp$0.then) {
								request$jscomp$1.status = 0;
								task.thenableState = thrownValue === SuspenseException ? getThenableStateAfterSuspending() : null;
								var ping$jscomp$0 = task.ping;
								x$jscomp$0.then(ping$jscomp$0, ping$jscomp$0);
							} else {
								var errorInfo$jscomp$0 = getThrownInfo(task.componentStack);
								task.abortSet.delete(task);
								request$jscomp$1.status = 4;
								var boundary$jscomp$0 = task.blockedBoundary, row = task.row;
								null !== row && 0 === --row.pendingTasks && finishSuspenseListRow(request, row);
								request.allPendingTasks--;
								request$jscomp$0 = logRecoverableError(request, x$jscomp$0, errorInfo$jscomp$0);
								if (null === boundary$jscomp$0) fatalError(request, x$jscomp$0);
								else if (boundary$jscomp$0.pendingTasks--, 4 !== boundary$jscomp$0.status) {
									boundary$jscomp$0.status = 4;
									boundary$jscomp$0.errorDigest = request$jscomp$0;
									untrackBoundary(request, boundary$jscomp$0);
									var boundaryRow = boundary$jscomp$0.row;
									null !== boundaryRow && 0 === --boundaryRow.pendingTasks && finishSuspenseListRow(request, boundaryRow);
									boundary$jscomp$0.parentFlushed && request.clientRenderedBoundaries.push(boundary$jscomp$0);
									0 === request.pendingRootTasks && null === request.trackedPostpones && null !== boundary$jscomp$0.contentPreamble && preparePreamble(request);
								}
								0 === request.allPendingTasks && completeAll(request);
							}
						}
					}
				}
				pingedTasks.splice(0, i);
				null !== request$jscomp$2.destination && flushCompletedQueues(request$jscomp$2, request$jscomp$2.destination);
			} catch (error) {
				logRecoverableError(request$jscomp$2, error, {}), fatalError(request$jscomp$2, error);
			} finally {
				currentResumableState = prevResumableState, ReactSharedInternals.H = prevDispatcher, ReactSharedInternals.A = prevAsyncDispatcher, prevDispatcher === HooksDispatcher && switchContext(prevContext), currentRequest = prevRequest;
			}
		}
	}
	function preparePreambleFromSubtree(request, segment, collectedPreambleSegments) {
		segment.preambleChildren.length && collectedPreambleSegments.push(segment.preambleChildren);
		for (var pendingPreambles = !1, i = 0; i < segment.children.length; i++) pendingPreambles = preparePreambleFromSegment(request, segment.children[i], collectedPreambleSegments) || pendingPreambles;
		return pendingPreambles;
	}
	function preparePreambleFromSegment(request, segment, collectedPreambleSegments) {
		var boundary = segment.boundary;
		if (null === boundary) return preparePreambleFromSubtree(request, segment, collectedPreambleSegments);
		var preamble = boundary.contentPreamble, fallbackPreamble = boundary.fallbackPreamble;
		if (null === preamble || null === fallbackPreamble) return !1;
		switch (boundary.status) {
			case 1:
				hoistPreambleState(request.renderState, preamble);
				request.byteSize += boundary.byteSize;
				segment = boundary.completedSegments[0];
				if (!segment) throw Error("A previously unvisited boundary must have exactly one root segment. This is a bug in React.");
				return preparePreambleFromSubtree(request, segment, collectedPreambleSegments);
			case 5: if (null !== request.trackedPostpones) return !0;
			case 4: if (1 === segment.status) return hoistPreambleState(request.renderState, fallbackPreamble), preparePreambleFromSubtree(request, segment, collectedPreambleSegments);
			default: return !0;
		}
	}
	function preparePreamble(request) {
		if (request.completedRootSegment && null === request.completedPreambleSegments) {
			var collectedPreambleSegments = [], originalRequestByteSize = request.byteSize, hasPendingPreambles = preparePreambleFromSegment(request, request.completedRootSegment, collectedPreambleSegments), preamble = request.renderState.preamble;
			!1 === hasPendingPreambles || preamble.headChunks && preamble.bodyChunks ? request.completedPreambleSegments = collectedPreambleSegments : request.byteSize = originalRequestByteSize;
		}
	}
	function flushSubtree(request, destination, segment, hoistableState) {
		segment.parentFlushed = !0;
		switch (segment.status) {
			case 0: segment.id = request.nextSegmentId++;
			case 5: return hoistableState = segment.id, segment.lastPushedText = !1, segment.textEmbedded = !1, request = request.renderState, writeChunk(destination, placeholder1), writeChunk(destination, request.placeholderPrefix), request = stringToChunk(hoistableState.toString(16)), writeChunk(destination, request), writeChunkAndReturn(destination, placeholder2);
			case 1:
				segment.status = 2;
				var r = !0, chunks = segment.chunks, chunkIdx = 0;
				segment = segment.children;
				for (var childIdx = 0; childIdx < segment.length; childIdx++) {
					for (r = segment[childIdx]; chunkIdx < r.index; chunkIdx++) writeChunk(destination, chunks[chunkIdx]);
					r = flushSegment(request, destination, r, hoistableState);
				}
				for (; chunkIdx < chunks.length - 1; chunkIdx++) writeChunk(destination, chunks[chunkIdx]);
				chunkIdx < chunks.length && (r = writeChunkAndReturn(destination, chunks[chunkIdx]));
				return r;
			case 3: return !0;
			default: throw Error("Aborted, errored or already flushed boundaries should not be flushed again. This is a bug in React.");
		}
	}
	var flushedByteSize = 0;
	function flushSegment(request, destination, segment, hoistableState) {
		var boundary = segment.boundary;
		if (null === boundary) return flushSubtree(request, destination, segment, hoistableState);
		boundary.parentFlushed = !0;
		if (4 === boundary.status) {
			var row = boundary.row;
			null !== row && 0 === --row.pendingTasks && finishSuspenseListRow(request, row);
			boundary = boundary.errorDigest;
			writeChunkAndReturn(destination, startClientRenderedSuspenseBoundary);
			writeChunk(destination, clientRenderedSuspenseBoundaryError1);
			boundary && (writeChunk(destination, clientRenderedSuspenseBoundaryError1A), writeChunk(destination, stringToChunk(escapeTextForBrowser(boundary))), writeChunk(destination, clientRenderedSuspenseBoundaryErrorAttrInterstitial));
			writeChunkAndReturn(destination, clientRenderedSuspenseBoundaryError2);
			flushSubtree(request, destination, segment, hoistableState);
		} else if (1 !== boundary.status) 0 === boundary.status && (boundary.rootSegmentID = request.nextSegmentId++), 0 < boundary.completedSegments.length && request.partialBoundaries.push(boundary), writeStartPendingSuspenseBoundary(destination, request.renderState, boundary.rootSegmentID), hoistableState && hoistHoistables(hoistableState, boundary.fallbackState), flushSubtree(request, destination, segment, hoistableState);
		else if (!flushingPartialBoundaries && isEligibleForOutlining(request, boundary) && (flushedByteSize + boundary.byteSize > request.progressiveChunkSize || hasSuspenseyContent(boundary.contentState))) boundary.rootSegmentID = request.nextSegmentId++, request.completedBoundaries.push(boundary), writeStartPendingSuspenseBoundary(destination, request.renderState, boundary.rootSegmentID), flushSubtree(request, destination, segment, hoistableState);
		else {
			flushedByteSize += boundary.byteSize;
			hoistableState && hoistHoistables(hoistableState, boundary.contentState);
			segment = boundary.row;
			null !== segment && isEligibleForOutlining(request, boundary) && 0 === --segment.pendingTasks && finishSuspenseListRow(request, segment);
			writeChunkAndReturn(destination, startCompletedSuspenseBoundary);
			segment = boundary.completedSegments;
			if (1 !== segment.length) throw Error("A previously unvisited boundary must have exactly one root segment. This is a bug in React.");
			flushSegment(request, destination, segment[0], hoistableState);
		}
		return writeChunkAndReturn(destination, endSuspenseBoundary);
	}
	function flushSegmentContainer(request, destination, segment, hoistableState) {
		writeStartSegment(destination, request.renderState, segment.parentFormatContext, segment.id);
		flushSegment(request, destination, segment, hoistableState);
		return writeEndSegment(destination, segment.parentFormatContext);
	}
	function flushCompletedBoundary(request, destination, boundary) {
		flushedByteSize = boundary.byteSize;
		for (var completedSegments = boundary.completedSegments, i = 0; i < completedSegments.length; i++) flushPartiallyCompletedSegment(request, destination, boundary, completedSegments[i]);
		completedSegments.length = 0;
		completedSegments = boundary.row;
		null !== completedSegments && isEligibleForOutlining(request, boundary) && 0 === --completedSegments.pendingTasks && finishSuspenseListRow(request, completedSegments);
		writeHoistablesForBoundary(destination, boundary.contentState, request.renderState);
		completedSegments = request.resumableState;
		request = request.renderState;
		i = boundary.rootSegmentID;
		boundary = boundary.contentState;
		var requiresStyleInsertion = request.stylesToHoist;
		request.stylesToHoist = !1;
		writeChunk(destination, request.startInlineScript);
		writeChunk(destination, endOfStartTag);
		requiresStyleInsertion ? (0 === (completedSegments.instructions & 4) && (completedSegments.instructions |= 4, writeChunk(destination, clientRenderScriptFunctionOnly)), 0 === (completedSegments.instructions & 2) && (completedSegments.instructions |= 2, writeChunk(destination, completeBoundaryScriptFunctionOnly)), 0 === (completedSegments.instructions & 8) ? (completedSegments.instructions |= 8, writeChunk(destination, completeBoundaryWithStylesScript1FullPartial)) : writeChunk(destination, completeBoundaryWithStylesScript1Partial)) : (0 === (completedSegments.instructions & 2) && (completedSegments.instructions |= 2, writeChunk(destination, completeBoundaryScriptFunctionOnly)), writeChunk(destination, completeBoundaryScript1Partial));
		completedSegments = stringToChunk(i.toString(16));
		writeChunk(destination, request.boundaryPrefix);
		writeChunk(destination, completedSegments);
		writeChunk(destination, completeBoundaryScript2);
		writeChunk(destination, request.segmentPrefix);
		writeChunk(destination, completedSegments);
		requiresStyleInsertion ? (writeChunk(destination, completeBoundaryScript3a), writeStyleResourceDependenciesInJS(destination, boundary)) : writeChunk(destination, completeBoundaryScript3b);
		boundary = writeChunkAndReturn(destination, completeBoundaryScriptEnd);
		return writeBootstrap(destination, request) && boundary;
	}
	function flushPartiallyCompletedSegment(request, destination, boundary, segment) {
		if (2 === segment.status) return !0;
		var hoistableState = boundary.contentState, segmentID = segment.id;
		if (-1 === segmentID) {
			if (-1 === (segment.id = boundary.rootSegmentID)) throw Error("A root segment ID must have been assigned by now. This is a bug in React.");
			return flushSegmentContainer(request, destination, segment, hoistableState);
		}
		if (segmentID === boundary.rootSegmentID) return flushSegmentContainer(request, destination, segment, hoistableState);
		flushSegmentContainer(request, destination, segment, hoistableState);
		boundary = request.resumableState;
		request = request.renderState;
		writeChunk(destination, request.startInlineScript);
		writeChunk(destination, endOfStartTag);
		0 === (boundary.instructions & 1) ? (boundary.instructions |= 1, writeChunk(destination, completeSegmentScript1Full)) : writeChunk(destination, completeSegmentScript1Partial);
		writeChunk(destination, request.segmentPrefix);
		segmentID = stringToChunk(segmentID.toString(16));
		writeChunk(destination, segmentID);
		writeChunk(destination, completeSegmentScript2);
		writeChunk(destination, request.placeholderPrefix);
		writeChunk(destination, segmentID);
		destination = writeChunkAndReturn(destination, completeSegmentScriptEnd);
		return destination;
	}
	var flushingPartialBoundaries = !1;
	function flushCompletedQueues(request, destination) {
		currentView = /* @__PURE__ */ new Uint8Array(2048);
		writtenBytes = 0;
		try {
			if (!(0 < request.pendingRootTasks)) {
				var i, completedRootSegment = request.completedRootSegment;
				if (null !== completedRootSegment) {
					if (5 === completedRootSegment.status) return;
					var completedPreambleSegments = request.completedPreambleSegments;
					if (null === completedPreambleSegments) return;
					flushedByteSize = request.byteSize;
					var resumableState = request.resumableState, renderState = request.renderState, preamble = renderState.preamble, htmlChunks = preamble.htmlChunks, headChunks = preamble.headChunks, i$jscomp$0;
					if (htmlChunks) {
						for (i$jscomp$0 = 0; i$jscomp$0 < htmlChunks.length; i$jscomp$0++) writeChunk(destination, htmlChunks[i$jscomp$0]);
						if (headChunks) for (i$jscomp$0 = 0; i$jscomp$0 < headChunks.length; i$jscomp$0++) writeChunk(destination, headChunks[i$jscomp$0]);
						else writeChunk(destination, startChunkForTag("head")), writeChunk(destination, endOfStartTag);
					} else if (headChunks) for (i$jscomp$0 = 0; i$jscomp$0 < headChunks.length; i$jscomp$0++) writeChunk(destination, headChunks[i$jscomp$0]);
					var charsetChunks = renderState.charsetChunks;
					for (i$jscomp$0 = 0; i$jscomp$0 < charsetChunks.length; i$jscomp$0++) writeChunk(destination, charsetChunks[i$jscomp$0]);
					charsetChunks.length = 0;
					renderState.preconnects.forEach(flushResource, destination);
					renderState.preconnects.clear();
					var viewportChunks = renderState.viewportChunks;
					for (i$jscomp$0 = 0; i$jscomp$0 < viewportChunks.length; i$jscomp$0++) writeChunk(destination, viewportChunks[i$jscomp$0]);
					viewportChunks.length = 0;
					renderState.fontPreloads.forEach(flushResource, destination);
					renderState.fontPreloads.clear();
					renderState.highImagePreloads.forEach(flushResource, destination);
					renderState.highImagePreloads.clear();
					currentlyFlushingRenderState = renderState;
					renderState.styles.forEach(flushStylesInPreamble, destination);
					currentlyFlushingRenderState = null;
					var importMapChunks = renderState.importMapChunks;
					for (i$jscomp$0 = 0; i$jscomp$0 < importMapChunks.length; i$jscomp$0++) writeChunk(destination, importMapChunks[i$jscomp$0]);
					importMapChunks.length = 0;
					renderState.bootstrapScripts.forEach(flushResource, destination);
					renderState.scripts.forEach(flushResource, destination);
					renderState.scripts.clear();
					renderState.bulkPreloads.forEach(flushResource, destination);
					renderState.bulkPreloads.clear();
					htmlChunks || headChunks || (resumableState.instructions |= 32);
					var hoistableChunks = renderState.hoistableChunks;
					for (i$jscomp$0 = 0; i$jscomp$0 < hoistableChunks.length; i$jscomp$0++) writeChunk(destination, hoistableChunks[i$jscomp$0]);
					for (resumableState = hoistableChunks.length = 0; resumableState < completedPreambleSegments.length; resumableState++) {
						var segments = completedPreambleSegments[resumableState];
						for (renderState = 0; renderState < segments.length; renderState++) flushSegment(request, destination, segments[renderState], null);
					}
					var preamble$jscomp$0 = request.renderState.preamble, headChunks$jscomp$0 = preamble$jscomp$0.headChunks;
					(preamble$jscomp$0.htmlChunks || headChunks$jscomp$0) && writeChunk(destination, endChunkForTag("head"));
					var bodyChunks = preamble$jscomp$0.bodyChunks;
					if (bodyChunks) for (completedPreambleSegments = 0; completedPreambleSegments < bodyChunks.length; completedPreambleSegments++) writeChunk(destination, bodyChunks[completedPreambleSegments]);
					flushSegment(request, destination, completedRootSegment, null);
					request.completedRootSegment = null;
					var renderState$jscomp$0 = request.renderState;
					if (0 !== request.allPendingTasks || 0 !== request.clientRenderedBoundaries.length || 0 !== request.completedBoundaries.length || null !== request.trackedPostpones && (0 !== request.trackedPostpones.rootNodes.length || null !== request.trackedPostpones.rootSlots)) {
						var resumableState$jscomp$0 = request.resumableState;
						if (0 === (resumableState$jscomp$0.instructions & 64)) {
							resumableState$jscomp$0.instructions |= 64;
							writeChunk(destination, renderState$jscomp$0.startInlineScript);
							if (0 === (resumableState$jscomp$0.instructions & 32)) {
								resumableState$jscomp$0.instructions |= 32;
								var shellId = "_" + resumableState$jscomp$0.idPrefix + "R_";
								writeChunk(destination, completedShellIdAttributeStart);
								writeChunk(destination, stringToChunk(escapeTextForBrowser(shellId)));
								writeChunk(destination, attributeEnd);
							}
							writeChunk(destination, endOfStartTag);
							writeChunk(destination, shellTimeRuntimeScript);
							writeChunkAndReturn(destination, endInlineScript);
						}
					}
					writeBootstrap(destination, renderState$jscomp$0);
				}
				var renderState$jscomp$1 = request.renderState;
				completedRootSegment = 0;
				var viewportChunks$jscomp$0 = renderState$jscomp$1.viewportChunks;
				for (completedRootSegment = 0; completedRootSegment < viewportChunks$jscomp$0.length; completedRootSegment++) writeChunk(destination, viewportChunks$jscomp$0[completedRootSegment]);
				viewportChunks$jscomp$0.length = 0;
				renderState$jscomp$1.preconnects.forEach(flushResource, destination);
				renderState$jscomp$1.preconnects.clear();
				renderState$jscomp$1.fontPreloads.forEach(flushResource, destination);
				renderState$jscomp$1.fontPreloads.clear();
				renderState$jscomp$1.highImagePreloads.forEach(flushResource, destination);
				renderState$jscomp$1.highImagePreloads.clear();
				renderState$jscomp$1.styles.forEach(preloadLateStyles, destination);
				renderState$jscomp$1.scripts.forEach(flushResource, destination);
				renderState$jscomp$1.scripts.clear();
				renderState$jscomp$1.bulkPreloads.forEach(flushResource, destination);
				renderState$jscomp$1.bulkPreloads.clear();
				var hoistableChunks$jscomp$0 = renderState$jscomp$1.hoistableChunks;
				for (completedRootSegment = 0; completedRootSegment < hoistableChunks$jscomp$0.length; completedRootSegment++) writeChunk(destination, hoistableChunks$jscomp$0[completedRootSegment]);
				hoistableChunks$jscomp$0.length = 0;
				var clientRenderedBoundaries = request.clientRenderedBoundaries;
				for (i = 0; i < clientRenderedBoundaries.length; i++) {
					var boundary = clientRenderedBoundaries[i];
					renderState$jscomp$1 = destination;
					var resumableState$jscomp$1 = request.resumableState, renderState$jscomp$2 = request.renderState, id = boundary.rootSegmentID, errorDigest = boundary.errorDigest;
					writeChunk(renderState$jscomp$1, renderState$jscomp$2.startInlineScript);
					writeChunk(renderState$jscomp$1, endOfStartTag);
					0 === (resumableState$jscomp$1.instructions & 4) ? (resumableState$jscomp$1.instructions |= 4, writeChunk(renderState$jscomp$1, clientRenderScript1Full)) : writeChunk(renderState$jscomp$1, clientRenderScript1Partial);
					writeChunk(renderState$jscomp$1, renderState$jscomp$2.boundaryPrefix);
					writeChunk(renderState$jscomp$1, stringToChunk(id.toString(16)));
					writeChunk(renderState$jscomp$1, clientRenderScript1A);
					errorDigest && (writeChunk(renderState$jscomp$1, clientRenderErrorScriptArgInterstitial), writeChunk(renderState$jscomp$1, stringToChunk(escapeJSStringsForInstructionScripts(errorDigest || ""))));
					var JSCompiler_inline_result = writeChunkAndReturn(renderState$jscomp$1, clientRenderScriptEnd);
					if (!JSCompiler_inline_result) {
						request.destination = null;
						i++;
						clientRenderedBoundaries.splice(0, i);
						return;
					}
				}
				clientRenderedBoundaries.splice(0, i);
				var completedBoundaries = request.completedBoundaries;
				for (i = 0; i < completedBoundaries.length; i++) if (!flushCompletedBoundary(request, destination, completedBoundaries[i])) {
					request.destination = null;
					i++;
					completedBoundaries.splice(0, i);
					return;
				}
				completedBoundaries.splice(0, i);
				completeWriting(destination);
				currentView = /* @__PURE__ */ new Uint8Array(2048);
				writtenBytes = 0;
				flushingPartialBoundaries = !0;
				var partialBoundaries = request.partialBoundaries;
				for (i = 0; i < partialBoundaries.length; i++) {
					var boundary$70 = partialBoundaries[i];
					a: {
						clientRenderedBoundaries = request;
						boundary = destination;
						flushedByteSize = boundary$70.byteSize;
						var completedSegments = boundary$70.completedSegments;
						for (JSCompiler_inline_result = 0; JSCompiler_inline_result < completedSegments.length; JSCompiler_inline_result++) if (!flushPartiallyCompletedSegment(clientRenderedBoundaries, boundary, boundary$70, completedSegments[JSCompiler_inline_result])) {
							JSCompiler_inline_result++;
							completedSegments.splice(0, JSCompiler_inline_result);
							var JSCompiler_inline_result$jscomp$0 = !1;
							break a;
						}
						completedSegments.splice(0, JSCompiler_inline_result);
						var row = boundary$70.row;
						null !== row && row.together && 1 === boundary$70.pendingTasks && (1 === row.pendingTasks ? unblockSuspenseListRow(clientRenderedBoundaries, row, row.hoistables) : row.pendingTasks--);
						JSCompiler_inline_result$jscomp$0 = writeHoistablesForBoundary(boundary, boundary$70.contentState, clientRenderedBoundaries.renderState);
					}
					if (!JSCompiler_inline_result$jscomp$0) {
						request.destination = null;
						i++;
						partialBoundaries.splice(0, i);
						return;
					}
				}
				partialBoundaries.splice(0, i);
				flushingPartialBoundaries = !1;
				var largeBoundaries = request.completedBoundaries;
				for (i = 0; i < largeBoundaries.length; i++) if (!flushCompletedBoundary(request, destination, largeBoundaries[i])) {
					request.destination = null;
					i++;
					largeBoundaries.splice(0, i);
					return;
				}
				largeBoundaries.splice(0, i);
			}
		} finally {
			flushingPartialBoundaries = !1, 0 === request.allPendingTasks && 0 === request.clientRenderedBoundaries.length && 0 === request.completedBoundaries.length ? (request.flushScheduled = !1, i = request.resumableState, i.hasBody && writeChunk(destination, endChunkForTag("body")), i.hasHtml && writeChunk(destination, endChunkForTag("html")), completeWriting(destination), request.status = 14, destination.close(), request.destination = null) : completeWriting(destination);
		}
	}
	function startWork(request) {
		request.flushScheduled = null !== request.destination;
		supportsRequestStorage ? scheduleMicrotask(function() {
			return requestStorage.run(request, performWork, request);
		}) : scheduleMicrotask(function() {
			return performWork(request);
		});
		setTimeout(function() {
			10 === request.status && (request.status = 11);
			null === request.trackedPostpones && (supportsRequestStorage ? requestStorage.run(request, enqueueEarlyPreloadsAfterInitialWork, request) : enqueueEarlyPreloadsAfterInitialWork(request));
		}, 0);
	}
	function enqueueEarlyPreloadsAfterInitialWork(request) {
		safelyEmitEarlyPreloads(request, 0 === request.pendingRootTasks);
	}
	function enqueueFlush(request) {
		!1 === request.flushScheduled && 0 === request.pingedTasks.length && null !== request.destination && (request.flushScheduled = !0, setTimeout(function() {
			var destination = request.destination;
			destination ? flushCompletedQueues(request, destination) : request.flushScheduled = !1;
		}, 0));
	}
	function startFlowing(request, destination) {
		if (13 === request.status) request.status = 14, closeWithError(destination, request.fatalError);
		else if (14 !== request.status && null === request.destination) {
			request.destination = destination;
			try {
				flushCompletedQueues(request, destination);
			} catch (error) {
				logRecoverableError(request, error, {}), fatalError(request, error);
			}
		}
	}
	function abort(request, reason) {
		if (11 === request.status || 10 === request.status) request.status = 12;
		try {
			var abortableTasks = request.abortableTasks;
			if (0 < abortableTasks.size) {
				var error = void 0 === reason ? Error("The render was aborted by the server without a reason.") : "object" === typeof reason && null !== reason && "function" === typeof reason.then ? Error("The render was aborted by the server with a promise.") : reason;
				request.fatalError = error;
				abortableTasks.forEach(function(task) {
					return abortTask(task, request, error);
				});
				abortableTasks.clear();
			}
			null !== request.destination && flushCompletedQueues(request, request.destination);
		} catch (error$72) {
			logRecoverableError(request, error$72, {}), fatalError(request, error$72);
		}
	}
	function addToReplayParent(node, parentKeyPath, trackedPostpones) {
		if (null === parentKeyPath) trackedPostpones.rootNodes.push(node);
		else {
			var workingMap = trackedPostpones.workingMap, parentNode = workingMap.get(parentKeyPath);
			void 0 === parentNode && (parentNode = [
				parentKeyPath[1],
				parentKeyPath[2],
				[],
				null
			], workingMap.set(parentKeyPath, parentNode), addToReplayParent(parentNode, parentKeyPath[0], trackedPostpones));
			parentNode[2].push(node);
		}
	}
	function getPostponedState(request) {
		var trackedPostpones = request.trackedPostpones;
		if (null === trackedPostpones || 0 === trackedPostpones.rootNodes.length && null === trackedPostpones.rootSlots) return request.trackedPostpones = null;
		if (null === request.completedRootSegment || 5 !== request.completedRootSegment.status && null !== request.completedPreambleSegments) {
			var nextSegmentId = request.nextSegmentId;
			var replaySlots = trackedPostpones.rootSlots;
			var resumableState = request.resumableState;
			resumableState.bootstrapScriptContent = void 0;
			resumableState.bootstrapScripts = void 0;
			resumableState.bootstrapModules = void 0;
		} else {
			nextSegmentId = 0;
			replaySlots = -1;
			resumableState = request.resumableState;
			var renderState = request.renderState;
			resumableState.nextFormID = 0;
			resumableState.hasBody = !1;
			resumableState.hasHtml = !1;
			resumableState.unknownResources = { font: renderState.resets.font };
			resumableState.dnsResources = renderState.resets.dns;
			resumableState.connectResources = renderState.resets.connect;
			resumableState.imageResources = renderState.resets.image;
			resumableState.styleResources = renderState.resets.style;
			resumableState.scriptResources = {};
			resumableState.moduleUnknownResources = {};
			resumableState.moduleScriptResources = {};
			resumableState.instructions = 0;
		}
		return {
			nextSegmentId,
			rootFormatContext: request.rootFormatContext,
			progressiveChunkSize: request.progressiveChunkSize,
			resumableState: request.resumableState,
			replayNodes: trackedPostpones.rootNodes,
			replaySlots
		};
	}
	function ensureCorrectIsomorphicReactVersion() {
		var isomorphicReactPackageVersion = React.version;
		if ("19.2.7" !== isomorphicReactPackageVersion) throw Error("Incompatible React versions: The \"react\" and \"react-dom\" packages must have the exact same version. Instead got:\n  - react:      " + (isomorphicReactPackageVersion + "\n  - react-dom:  19.2.7\nLearn more: https://react.dev/warnings/version-mismatch"));
	}
	ensureCorrectIsomorphicReactVersion();
	ensureCorrectIsomorphicReactVersion();
	exports.prerender = function(children, options) {
		return new Promise(function(resolve, reject) {
			var onHeaders = options ? options.onHeaders : void 0, onHeadersImpl;
			onHeaders && (onHeadersImpl = function(headersDescriptor) {
				onHeaders(new Headers(headersDescriptor));
			});
			var resources = createResumableState(options ? options.identifierPrefix : void 0, options ? options.unstable_externalRuntimeSrc : void 0, options ? options.bootstrapScriptContent : void 0, options ? options.bootstrapScripts : void 0, options ? options.bootstrapModules : void 0), request = createPrerenderRequest(children, resources, createRenderState(resources, void 0, options ? options.unstable_externalRuntimeSrc : void 0, options ? options.importMap : void 0, onHeadersImpl, options ? options.maxHeadersLength : void 0), createRootFormatContext(options ? options.namespaceURI : void 0), options ? options.progressiveChunkSize : void 0, options ? options.onError : void 0, function() {
				var stream = new ReadableStream({
					type: "bytes",
					pull: function(controller) {
						startFlowing(request, controller);
					},
					cancel: function(reason) {
						request.destination = null;
						abort(request, reason);
					}
				}, { highWaterMark: 0 });
				stream = {
					postponed: getPostponedState(request),
					prelude: stream
				};
				resolve(stream);
			}, void 0, void 0, reject, options ? options.onPostpone : void 0);
			if (options && options.signal) {
				var signal = options.signal;
				if (signal.aborted) abort(request, signal.reason);
				else {
					var listener = function() {
						abort(request, signal.reason);
						signal.removeEventListener("abort", listener);
					};
					signal.addEventListener("abort", listener);
				}
			}
			startWork(request);
		});
	};
	exports.renderToReadableStream = function(children, options) {
		return new Promise(function(resolve, reject) {
			var onFatalError, onAllReady, allReady = new Promise(function(res, rej) {
				onAllReady = res;
				onFatalError = rej;
			}), onHeaders = options ? options.onHeaders : void 0, onHeadersImpl;
			onHeaders && (onHeadersImpl = function(headersDescriptor) {
				onHeaders(new Headers(headersDescriptor));
			});
			var resumableState = createResumableState(options ? options.identifierPrefix : void 0, options ? options.unstable_externalRuntimeSrc : void 0, options ? options.bootstrapScriptContent : void 0, options ? options.bootstrapScripts : void 0, options ? options.bootstrapModules : void 0), request = createRequest(children, resumableState, createRenderState(resumableState, options ? options.nonce : void 0, options ? options.unstable_externalRuntimeSrc : void 0, options ? options.importMap : void 0, onHeadersImpl, options ? options.maxHeadersLength : void 0), createRootFormatContext(options ? options.namespaceURI : void 0), options ? options.progressiveChunkSize : void 0, options ? options.onError : void 0, onAllReady, function() {
				var stream = new ReadableStream({
					type: "bytes",
					pull: function(controller) {
						startFlowing(request, controller);
					},
					cancel: function(reason) {
						request.destination = null;
						abort(request, reason);
					}
				}, { highWaterMark: 0 });
				stream.allReady = allReady;
				resolve(stream);
			}, function(error) {
				allReady.catch(function() {});
				reject(error);
			}, onFatalError, options ? options.onPostpone : void 0, options ? options.formState : void 0);
			if (options && options.signal) {
				var signal = options.signal;
				if (signal.aborted) abort(request, signal.reason);
				else {
					var listener = function() {
						abort(request, signal.reason);
						signal.removeEventListener("abort", listener);
					};
					signal.addEventListener("abort", listener);
				}
			}
			startWork(request);
		});
	};
	exports.resume = function(children, postponedState, options) {
		return new Promise(function(resolve, reject) {
			var onFatalError, onAllReady, allReady = new Promise(function(res, rej) {
				onAllReady = res;
				onFatalError = rej;
			}), request = resumeRequest(children, postponedState, createRenderState(postponedState.resumableState, options ? options.nonce : void 0, void 0, void 0, void 0, void 0), options ? options.onError : void 0, onAllReady, function() {
				var stream = new ReadableStream({
					type: "bytes",
					pull: function(controller) {
						startFlowing(request, controller);
					},
					cancel: function(reason) {
						request.destination = null;
						abort(request, reason);
					}
				}, { highWaterMark: 0 });
				stream.allReady = allReady;
				resolve(stream);
			}, function(error) {
				allReady.catch(function() {});
				reject(error);
			}, onFatalError, options ? options.onPostpone : void 0);
			if (options && options.signal) {
				var signal = options.signal;
				if (signal.aborted) abort(request, signal.reason);
				else {
					var listener = function() {
						abort(request, signal.reason);
						signal.removeEventListener("abort", listener);
					};
					signal.addEventListener("abort", listener);
				}
			}
			startWork(request);
		});
	};
	exports.resumeAndPrerender = function(children, postponedState, options) {
		return new Promise(function(resolve, reject) {
			var request = resumeAndPrerenderRequest(children, postponedState, createRenderState(postponedState.resumableState, void 0, void 0, void 0, void 0, void 0), options ? options.onError : void 0, function() {
				var stream = new ReadableStream({
					type: "bytes",
					pull: function(controller) {
						startFlowing(request, controller);
					},
					cancel: function(reason) {
						request.destination = null;
						abort(request, reason);
					}
				}, { highWaterMark: 0 });
				stream = {
					postponed: getPostponedState(request),
					prelude: stream
				};
				resolve(stream);
			}, void 0, void 0, reject, options ? options.onPostpone : void 0);
			if (options && options.signal) {
				var signal = options.signal;
				if (signal.aborted) abort(request, signal.reason);
				else {
					var listener = function() {
						abort(request, signal.reason);
						signal.removeEventListener("abort", listener);
					};
					signal.addEventListener("abort", listener);
				}
			}
			startWork(request);
		});
	};
	exports.version = "19.2.7";
}));
//#endregion
//#region ../../node_modules/.pnpm/react-dom@19.2.7_react@19.2.7/node_modules/react-dom/cjs/react-dom-server-legacy.browser.production.js
/**
* @license React
* react-dom-server-legacy.browser.production.js
*
* Copyright (c) Meta Platforms, Inc. and affiliates.
*
* This source code is licensed under the MIT license found in the
* LICENSE file in the root directory of this source tree.
*/
var require_react_dom_server_legacy_browser_production = /* @__PURE__ */ __commonJSMin(((exports) => {
	var React = require_react();
	var ReactDOM = require_react_dom();
	function formatProdErrorMessage(code) {
		var url = "https://react.dev/errors/" + code;
		if (1 < arguments.length) {
			url += "?args[]=" + encodeURIComponent(arguments[1]);
			for (var i = 2; i < arguments.length; i++) url += "&args[]=" + encodeURIComponent(arguments[i]);
		}
		return "Minified React error #" + code + "; visit " + url + " for the full message or use the non-minified dev environment for full errors and additional helpful warnings.";
	}
	var REACT_ELEMENT_TYPE = Symbol.for("react.transitional.element");
	var REACT_PORTAL_TYPE = Symbol.for("react.portal");
	var REACT_FRAGMENT_TYPE = Symbol.for("react.fragment");
	var REACT_STRICT_MODE_TYPE = Symbol.for("react.strict_mode");
	var REACT_PROFILER_TYPE = Symbol.for("react.profiler");
	var REACT_CONSUMER_TYPE = Symbol.for("react.consumer");
	var REACT_CONTEXT_TYPE = Symbol.for("react.context");
	var REACT_FORWARD_REF_TYPE = Symbol.for("react.forward_ref");
	var REACT_SUSPENSE_TYPE = Symbol.for("react.suspense");
	var REACT_SUSPENSE_LIST_TYPE = Symbol.for("react.suspense_list");
	var REACT_MEMO_TYPE = Symbol.for("react.memo");
	var REACT_LAZY_TYPE = Symbol.for("react.lazy");
	var REACT_SCOPE_TYPE = Symbol.for("react.scope");
	var REACT_ACTIVITY_TYPE = Symbol.for("react.activity");
	var REACT_LEGACY_HIDDEN_TYPE = Symbol.for("react.legacy_hidden");
	var REACT_MEMO_CACHE_SENTINEL = Symbol.for("react.memo_cache_sentinel");
	var REACT_VIEW_TRANSITION_TYPE = Symbol.for("react.view_transition");
	var MAYBE_ITERATOR_SYMBOL = Symbol.iterator;
	function getIteratorFn(maybeIterable) {
		if (null === maybeIterable || "object" !== typeof maybeIterable) return null;
		maybeIterable = MAYBE_ITERATOR_SYMBOL && maybeIterable[MAYBE_ITERATOR_SYMBOL] || maybeIterable["@@iterator"];
		return "function" === typeof maybeIterable ? maybeIterable : null;
	}
	var isArrayImpl = Array.isArray;
	function murmurhash3_32_gc(key, seed) {
		var remainder = key.length & 3;
		var bytes = key.length - remainder;
		var h1 = seed;
		for (seed = 0; seed < bytes;) {
			var k1 = key.charCodeAt(seed) & 255 | (key.charCodeAt(++seed) & 255) << 8 | (key.charCodeAt(++seed) & 255) << 16 | (key.charCodeAt(++seed) & 255) << 24;
			++seed;
			k1 = 3432918353 * (k1 & 65535) + ((3432918353 * (k1 >>> 16) & 65535) << 16) & 4294967295;
			k1 = k1 << 15 | k1 >>> 17;
			k1 = 461845907 * (k1 & 65535) + ((461845907 * (k1 >>> 16) & 65535) << 16) & 4294967295;
			h1 ^= k1;
			h1 = h1 << 13 | h1 >>> 19;
			h1 = 5 * (h1 & 65535) + ((5 * (h1 >>> 16) & 65535) << 16) & 4294967295;
			h1 = (h1 & 65535) + 27492 + (((h1 >>> 16) + 58964 & 65535) << 16);
		}
		k1 = 0;
		switch (remainder) {
			case 3: k1 ^= (key.charCodeAt(seed + 2) & 255) << 16;
			case 2: k1 ^= (key.charCodeAt(seed + 1) & 255) << 8;
			case 1: k1 ^= key.charCodeAt(seed) & 255, k1 = 3432918353 * (k1 & 65535) + ((3432918353 * (k1 >>> 16) & 65535) << 16) & 4294967295, k1 = k1 << 15 | k1 >>> 17, h1 ^= 461845907 * (k1 & 65535) + ((461845907 * (k1 >>> 16) & 65535) << 16) & 4294967295;
		}
		h1 ^= key.length;
		h1 ^= h1 >>> 16;
		h1 = 2246822507 * (h1 & 65535) + ((2246822507 * (h1 >>> 16) & 65535) << 16) & 4294967295;
		h1 ^= h1 >>> 13;
		h1 = 3266489909 * (h1 & 65535) + ((3266489909 * (h1 >>> 16) & 65535) << 16) & 4294967295;
		return (h1 ^ h1 >>> 16) >>> 0;
	}
	var assign = Object.assign;
	var hasOwnProperty = Object.prototype.hasOwnProperty;
	var VALID_ATTRIBUTE_NAME_REGEX = RegExp("^[:A-Z_a-z\\u00C0-\\u00D6\\u00D8-\\u00F6\\u00F8-\\u02FF\\u0370-\\u037D\\u037F-\\u1FFF\\u200C-\\u200D\\u2070-\\u218F\\u2C00-\\u2FEF\\u3001-\\uD7FF\\uF900-\\uFDCF\\uFDF0-\\uFFFD][:A-Z_a-z\\u00C0-\\u00D6\\u00D8-\\u00F6\\u00F8-\\u02FF\\u0370-\\u037D\\u037F-\\u1FFF\\u200C-\\u200D\\u2070-\\u218F\\u2C00-\\u2FEF\\u3001-\\uD7FF\\uF900-\\uFDCF\\uFDF0-\\uFFFD\\-.0-9\\u00B7\\u0300-\\u036F\\u203F-\\u2040]*$");
	var illegalAttributeNameCache = {};
	var validatedAttributeNameCache = {};
	function isAttributeNameSafe(attributeName) {
		if (hasOwnProperty.call(validatedAttributeNameCache, attributeName)) return !0;
		if (hasOwnProperty.call(illegalAttributeNameCache, attributeName)) return !1;
		if (VALID_ATTRIBUTE_NAME_REGEX.test(attributeName)) return validatedAttributeNameCache[attributeName] = !0;
		illegalAttributeNameCache[attributeName] = !0;
		return !1;
	}
	var unitlessNumbers = new Set("animationIterationCount aspectRatio borderImageOutset borderImageSlice borderImageWidth boxFlex boxFlexGroup boxOrdinalGroup columnCount columns flex flexGrow flexPositive flexShrink flexNegative flexOrder gridArea gridRow gridRowEnd gridRowSpan gridRowStart gridColumn gridColumnEnd gridColumnSpan gridColumnStart fontWeight lineClamp lineHeight opacity order orphans scale tabSize widows zIndex zoom fillOpacity floodOpacity stopOpacity strokeDasharray strokeDashoffset strokeMiterlimit strokeOpacity strokeWidth MozAnimationIterationCount MozBoxFlex MozBoxFlexGroup MozLineClamp msAnimationIterationCount msFlex msZoom msFlexGrow msFlexNegative msFlexOrder msFlexPositive msFlexShrink msGridColumn msGridColumnSpan msGridRow msGridRowSpan WebkitAnimationIterationCount WebkitBoxFlex WebKitBoxFlexGroup WebkitBoxOrdinalGroup WebkitColumnCount WebkitColumns WebkitFlex WebkitFlexGrow WebkitFlexPositive WebkitFlexShrink WebkitLineClamp".split(" "));
	var aliases = /* @__PURE__ */ new Map([
		["acceptCharset", "accept-charset"],
		["htmlFor", "for"],
		["httpEquiv", "http-equiv"],
		["crossOrigin", "crossorigin"],
		["accentHeight", "accent-height"],
		["alignmentBaseline", "alignment-baseline"],
		["arabicForm", "arabic-form"],
		["baselineShift", "baseline-shift"],
		["capHeight", "cap-height"],
		["clipPath", "clip-path"],
		["clipRule", "clip-rule"],
		["colorInterpolation", "color-interpolation"],
		["colorInterpolationFilters", "color-interpolation-filters"],
		["colorProfile", "color-profile"],
		["colorRendering", "color-rendering"],
		["dominantBaseline", "dominant-baseline"],
		["enableBackground", "enable-background"],
		["fillOpacity", "fill-opacity"],
		["fillRule", "fill-rule"],
		["floodColor", "flood-color"],
		["floodOpacity", "flood-opacity"],
		["fontFamily", "font-family"],
		["fontSize", "font-size"],
		["fontSizeAdjust", "font-size-adjust"],
		["fontStretch", "font-stretch"],
		["fontStyle", "font-style"],
		["fontVariant", "font-variant"],
		["fontWeight", "font-weight"],
		["glyphName", "glyph-name"],
		["glyphOrientationHorizontal", "glyph-orientation-horizontal"],
		["glyphOrientationVertical", "glyph-orientation-vertical"],
		["horizAdvX", "horiz-adv-x"],
		["horizOriginX", "horiz-origin-x"],
		["imageRendering", "image-rendering"],
		["letterSpacing", "letter-spacing"],
		["lightingColor", "lighting-color"],
		["markerEnd", "marker-end"],
		["markerMid", "marker-mid"],
		["markerStart", "marker-start"],
		["overlinePosition", "overline-position"],
		["overlineThickness", "overline-thickness"],
		["paintOrder", "paint-order"],
		["panose-1", "panose-1"],
		["pointerEvents", "pointer-events"],
		["renderingIntent", "rendering-intent"],
		["shapeRendering", "shape-rendering"],
		["stopColor", "stop-color"],
		["stopOpacity", "stop-opacity"],
		["strikethroughPosition", "strikethrough-position"],
		["strikethroughThickness", "strikethrough-thickness"],
		["strokeDasharray", "stroke-dasharray"],
		["strokeDashoffset", "stroke-dashoffset"],
		["strokeLinecap", "stroke-linecap"],
		["strokeLinejoin", "stroke-linejoin"],
		["strokeMiterlimit", "stroke-miterlimit"],
		["strokeOpacity", "stroke-opacity"],
		["strokeWidth", "stroke-width"],
		["textAnchor", "text-anchor"],
		["textDecoration", "text-decoration"],
		["textRendering", "text-rendering"],
		["transformOrigin", "transform-origin"],
		["underlinePosition", "underline-position"],
		["underlineThickness", "underline-thickness"],
		["unicodeBidi", "unicode-bidi"],
		["unicodeRange", "unicode-range"],
		["unitsPerEm", "units-per-em"],
		["vAlphabetic", "v-alphabetic"],
		["vHanging", "v-hanging"],
		["vIdeographic", "v-ideographic"],
		["vMathematical", "v-mathematical"],
		["vectorEffect", "vector-effect"],
		["vertAdvY", "vert-adv-y"],
		["vertOriginX", "vert-origin-x"],
		["vertOriginY", "vert-origin-y"],
		["wordSpacing", "word-spacing"],
		["writingMode", "writing-mode"],
		["xmlnsXlink", "xmlns:xlink"],
		["xHeight", "x-height"]
	]);
	var matchHtmlRegExp = /["'&<>]/;
	function escapeTextForBrowser(text) {
		if ("boolean" === typeof text || "number" === typeof text || "bigint" === typeof text) return "" + text;
		text = "" + text;
		var match = matchHtmlRegExp.exec(text);
		if (match) {
			var html = "", index, lastIndex = 0;
			for (index = match.index; index < text.length; index++) {
				switch (text.charCodeAt(index)) {
					case 34:
						match = "&quot;";
						break;
					case 38:
						match = "&amp;";
						break;
					case 39:
						match = "&#x27;";
						break;
					case 60:
						match = "&lt;";
						break;
					case 62:
						match = "&gt;";
						break;
					default: continue;
				}
				lastIndex !== index && (html += text.slice(lastIndex, index));
				lastIndex = index + 1;
				html += match;
			}
			text = lastIndex !== index ? html + text.slice(lastIndex, index) : html;
		}
		return text;
	}
	var uppercasePattern = /([A-Z])/g;
	var msPattern = /^ms-/;
	var isJavaScriptProtocol = /^[\u0000-\u001F ]*j[\r\n\t]*a[\r\n\t]*v[\r\n\t]*a[\r\n\t]*s[\r\n\t]*c[\r\n\t]*r[\r\n\t]*i[\r\n\t]*p[\r\n\t]*t[\r\n\t]*:/i;
	function sanitizeURL(url) {
		return isJavaScriptProtocol.test("" + url) ? "javascript:throw new Error('React has blocked a javascript: URL as a security precaution.')" : url;
	}
	var ReactSharedInternals = React.__CLIENT_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE;
	var ReactDOMSharedInternals = ReactDOM.__DOM_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE;
	var sharedNotPendingObject = {
		pending: !1,
		data: null,
		method: null,
		action: null
	};
	var previousDispatcher = ReactDOMSharedInternals.d;
	ReactDOMSharedInternals.d = {
		f: previousDispatcher.f,
		r: previousDispatcher.r,
		D: prefetchDNS,
		C: preconnect,
		L: preload,
		m: preloadModule,
		X: preinitScript,
		S: preinitStyle,
		M: preinitModuleScript
	};
	var PRELOAD_NO_CREDS = [];
	var currentlyFlushingRenderState = null;
	var scriptRegex = /(<\/|<)(s)(cript)/gi;
	function scriptReplacer(match, prefix, s, suffix) {
		return "" + prefix + ("s" === s ? "\\u0073" : "\\u0053") + suffix;
	}
	function createResumableState(identifierPrefix, externalRuntimeConfig, bootstrapScriptContent, bootstrapScripts, bootstrapModules) {
		return {
			idPrefix: void 0 === identifierPrefix ? "" : identifierPrefix,
			nextFormID: 0,
			streamingFormat: 0,
			bootstrapScriptContent,
			bootstrapScripts,
			bootstrapModules,
			instructions: 0,
			hasBody: !1,
			hasHtml: !1,
			unknownResources: {},
			dnsResources: {},
			connectResources: {
				default: {},
				anonymous: {},
				credentials: {}
			},
			imageResources: {},
			styleResources: {},
			scriptResources: {},
			moduleUnknownResources: {},
			moduleScriptResources: {}
		};
	}
	function createFormatContext(insertionMode, selectedValue, tagScope, viewTransition) {
		return {
			insertionMode,
			selectedValue,
			tagScope,
			viewTransition
		};
	}
	function getChildFormatContext(parentContext, type, props) {
		var subtreeScope = parentContext.tagScope & -25;
		switch (type) {
			case "noscript": return createFormatContext(2, null, subtreeScope | 1, null);
			case "select": return createFormatContext(2, null != props.value ? props.value : props.defaultValue, subtreeScope, null);
			case "svg": return createFormatContext(4, null, subtreeScope, null);
			case "picture": return createFormatContext(2, null, subtreeScope | 2, null);
			case "math": return createFormatContext(5, null, subtreeScope, null);
			case "foreignObject": return createFormatContext(2, null, subtreeScope, null);
			case "table": return createFormatContext(6, null, subtreeScope, null);
			case "thead":
			case "tbody":
			case "tfoot": return createFormatContext(7, null, subtreeScope, null);
			case "colgroup": return createFormatContext(9, null, subtreeScope, null);
			case "tr": return createFormatContext(8, null, subtreeScope, null);
			case "head":
				if (2 > parentContext.insertionMode) return createFormatContext(3, null, subtreeScope, null);
				break;
			case "html": if (0 === parentContext.insertionMode) return createFormatContext(1, null, subtreeScope, null);
		}
		return 6 <= parentContext.insertionMode || 2 > parentContext.insertionMode ? createFormatContext(2, null, subtreeScope, null) : parentContext.tagScope !== subtreeScope ? createFormatContext(parentContext.insertionMode, parentContext.selectedValue, subtreeScope, null) : parentContext;
	}
	function getSuspenseViewTransition(parentViewTransition) {
		return null === parentViewTransition ? null : {
			update: parentViewTransition.update,
			enter: "none",
			exit: "none",
			share: parentViewTransition.update,
			name: parentViewTransition.autoName,
			autoName: parentViewTransition.autoName,
			nameIdx: 0
		};
	}
	function getSuspenseFallbackFormatContext(resumableState, parentContext) {
		parentContext.tagScope & 32 && (resumableState.instructions |= 128);
		return createFormatContext(parentContext.insertionMode, parentContext.selectedValue, parentContext.tagScope | 12, getSuspenseViewTransition(parentContext.viewTransition));
	}
	function getSuspenseContentFormatContext(resumableState, parentContext) {
		resumableState = getSuspenseViewTransition(parentContext.viewTransition);
		var subtreeScope = parentContext.tagScope | 16;
		null !== resumableState && "none" !== resumableState.share && (subtreeScope |= 64);
		return createFormatContext(parentContext.insertionMode, parentContext.selectedValue, subtreeScope, resumableState);
	}
	var styleNameCache = /* @__PURE__ */ new Map();
	function pushStyleAttribute(target, style) {
		if ("object" !== typeof style) throw Error(formatProdErrorMessage(62));
		var isFirst = !0, styleName;
		for (styleName in style) if (hasOwnProperty.call(style, styleName)) {
			var styleValue = style[styleName];
			if (null != styleValue && "boolean" !== typeof styleValue && "" !== styleValue) {
				if (0 === styleName.indexOf("--")) {
					var nameChunk = escapeTextForBrowser(styleName);
					styleValue = escapeTextForBrowser(("" + styleValue).trim());
				} else nameChunk = styleNameCache.get(styleName), void 0 === nameChunk && (nameChunk = escapeTextForBrowser(styleName.replace(uppercasePattern, "-$1").toLowerCase().replace(msPattern, "-ms-")), styleNameCache.set(styleName, nameChunk)), styleValue = "number" === typeof styleValue ? 0 === styleValue || unitlessNumbers.has(styleName) ? "" + styleValue : styleValue + "px" : escapeTextForBrowser(("" + styleValue).trim());
				isFirst ? (isFirst = !1, target.push(" style=\"", nameChunk, ":", styleValue)) : target.push(";", nameChunk, ":", styleValue);
			}
		}
		isFirst || target.push("\"");
	}
	function pushBooleanAttribute(target, name, value) {
		value && "function" !== typeof value && "symbol" !== typeof value && target.push(" ", name, "=\"\"");
	}
	function pushStringAttribute(target, name, value) {
		"function" !== typeof value && "symbol" !== typeof value && "boolean" !== typeof value && target.push(" ", name, "=\"", escapeTextForBrowser(value), "\"");
	}
	var actionJavaScriptURL = escapeTextForBrowser("javascript:throw new Error('React form unexpectedly submitted.')");
	function pushAdditionalFormField(value, key) {
		this.push("<input type=\"hidden\"");
		validateAdditionalFormField(value);
		pushStringAttribute(this, "name", key);
		pushStringAttribute(this, "value", value);
		this.push("/>");
	}
	function validateAdditionalFormField(value) {
		if ("string" !== typeof value) throw Error(formatProdErrorMessage(480));
	}
	function getCustomFormFields(resumableState, formAction) {
		if ("function" === typeof formAction.$$FORM_ACTION) {
			var id = resumableState.nextFormID++;
			resumableState = resumableState.idPrefix + id;
			try {
				var customFields = formAction.$$FORM_ACTION(resumableState);
				if (customFields) customFields.data?.forEach(validateAdditionalFormField);
				return customFields;
			} catch (x) {
				if ("object" === typeof x && null !== x && "function" === typeof x.then) throw x;
			}
		}
		return null;
	}
	function pushFormActionAttribute(target, resumableState, renderState, formAction, formEncType, formMethod, formTarget, name) {
		var formData = null;
		if ("function" === typeof formAction) {
			var customFields = getCustomFormFields(resumableState, formAction);
			null !== customFields ? (name = customFields.name, formAction = customFields.action || "", formEncType = customFields.encType, formMethod = customFields.method, formTarget = customFields.target, formData = customFields.data) : (target.push(" ", "formAction", "=\"", actionJavaScriptURL, "\""), formTarget = formMethod = formEncType = formAction = name = null, injectFormReplayingRuntime(resumableState, renderState));
		}
		null != name && pushAttribute(target, "name", name);
		null != formAction && pushAttribute(target, "formAction", formAction);
		null != formEncType && pushAttribute(target, "formEncType", formEncType);
		null != formMethod && pushAttribute(target, "formMethod", formMethod);
		null != formTarget && pushAttribute(target, "formTarget", formTarget);
		return formData;
	}
	function pushAttribute(target, name, value) {
		switch (name) {
			case "className":
				pushStringAttribute(target, "class", value);
				break;
			case "tabIndex":
				pushStringAttribute(target, "tabindex", value);
				break;
			case "dir":
			case "role":
			case "viewBox":
			case "width":
			case "height":
				pushStringAttribute(target, name, value);
				break;
			case "style":
				pushStyleAttribute(target, value);
				break;
			case "src":
			case "href": if ("" === value) break;
			case "action":
			case "formAction":
				if (null == value || "function" === typeof value || "symbol" === typeof value || "boolean" === typeof value) break;
				value = sanitizeURL("" + value);
				target.push(" ", name, "=\"", escapeTextForBrowser(value), "\"");
				break;
			case "defaultValue":
			case "defaultChecked":
			case "innerHTML":
			case "suppressContentEditableWarning":
			case "suppressHydrationWarning":
			case "ref": break;
			case "autoFocus":
			case "multiple":
			case "muted":
				pushBooleanAttribute(target, name.toLowerCase(), value);
				break;
			case "xlinkHref":
				if ("function" === typeof value || "symbol" === typeof value || "boolean" === typeof value) break;
				value = sanitizeURL("" + value);
				target.push(" ", "xlink:href", "=\"", escapeTextForBrowser(value), "\"");
				break;
			case "contentEditable":
			case "spellCheck":
			case "draggable":
			case "value":
			case "autoReverse":
			case "externalResourcesRequired":
			case "focusable":
			case "preserveAlpha":
				"function" !== typeof value && "symbol" !== typeof value && target.push(" ", name, "=\"", escapeTextForBrowser(value), "\"");
				break;
			case "inert":
			case "allowFullScreen":
			case "async":
			case "autoPlay":
			case "controls":
			case "default":
			case "defer":
			case "disabled":
			case "disablePictureInPicture":
			case "disableRemotePlayback":
			case "formNoValidate":
			case "hidden":
			case "loop":
			case "noModule":
			case "noValidate":
			case "open":
			case "playsInline":
			case "readOnly":
			case "required":
			case "reversed":
			case "scoped":
			case "seamless":
			case "itemScope":
				value && "function" !== typeof value && "symbol" !== typeof value && target.push(" ", name, "=\"\"");
				break;
			case "capture":
			case "download":
				!0 === value ? target.push(" ", name, "=\"\"") : !1 !== value && "function" !== typeof value && "symbol" !== typeof value && target.push(" ", name, "=\"", escapeTextForBrowser(value), "\"");
				break;
			case "cols":
			case "rows":
			case "size":
			case "span":
				"function" !== typeof value && "symbol" !== typeof value && !isNaN(value) && 1 <= value && target.push(" ", name, "=\"", escapeTextForBrowser(value), "\"");
				break;
			case "rowSpan":
			case "start":
				"function" === typeof value || "symbol" === typeof value || isNaN(value) || target.push(" ", name, "=\"", escapeTextForBrowser(value), "\"");
				break;
			case "xlinkActuate":
				pushStringAttribute(target, "xlink:actuate", value);
				break;
			case "xlinkArcrole":
				pushStringAttribute(target, "xlink:arcrole", value);
				break;
			case "xlinkRole":
				pushStringAttribute(target, "xlink:role", value);
				break;
			case "xlinkShow":
				pushStringAttribute(target, "xlink:show", value);
				break;
			case "xlinkTitle":
				pushStringAttribute(target, "xlink:title", value);
				break;
			case "xlinkType":
				pushStringAttribute(target, "xlink:type", value);
				break;
			case "xmlBase":
				pushStringAttribute(target, "xml:base", value);
				break;
			case "xmlLang":
				pushStringAttribute(target, "xml:lang", value);
				break;
			case "xmlSpace":
				pushStringAttribute(target, "xml:space", value);
				break;
			default: if (!(2 < name.length) || "o" !== name[0] && "O" !== name[0] || "n" !== name[1] && "N" !== name[1]) {
				if (name = aliases.get(name) || name, isAttributeNameSafe(name)) {
					switch (typeof value) {
						case "function":
						case "symbol": return;
						case "boolean":
							var prefix$8 = name.toLowerCase().slice(0, 5);
							if ("data-" !== prefix$8 && "aria-" !== prefix$8) return;
					}
					target.push(" ", name, "=\"", escapeTextForBrowser(value), "\"");
				}
			}
		}
	}
	function pushInnerHTML(target, innerHTML, children) {
		if (null != innerHTML) {
			if (null != children) throw Error(formatProdErrorMessage(60));
			if ("object" !== typeof innerHTML || !("__html" in innerHTML)) throw Error(formatProdErrorMessage(61));
			innerHTML = innerHTML.__html;
			null !== innerHTML && void 0 !== innerHTML && target.push("" + innerHTML);
		}
	}
	function flattenOptionChildren(children) {
		var content = "";
		React.Children.forEach(children, function(child) {
			null != child && (content += child);
		});
		return content;
	}
	function injectFormReplayingRuntime(resumableState, renderState) {
		if (0 === (resumableState.instructions & 16)) {
			resumableState.instructions |= 16;
			var preamble = renderState.preamble, bootstrapChunks = renderState.bootstrapChunks;
			(preamble.htmlChunks || preamble.headChunks) && 0 === bootstrapChunks.length ? (bootstrapChunks.push(renderState.startInlineScript), pushCompletedShellIdAttribute(bootstrapChunks, resumableState), bootstrapChunks.push(">", "addEventListener(\"submit\",function(a){if(!a.defaultPrevented){var c=a.target,d=a.submitter,e=c.action,b=d;if(d){var f=d.getAttribute(\"formAction\");null!=f&&(e=f,b=null)}\"javascript:throw new Error('React form unexpectedly submitted.')\"===e&&(a.preventDefault(),b?(a=document.createElement(\"input\"),a.name=b.name,a.value=b.value,b.parentNode.insertBefore(a,b),b=new FormData(c),a.parentNode.removeChild(a)):b=new FormData(c),a=c.ownerDocument||c,(a.$$reactFormReplay=a.$$reactFormReplay||[]).push(c,d,b))}});", "<\/script>")) : bootstrapChunks.unshift(renderState.startInlineScript, ">", "addEventListener(\"submit\",function(a){if(!a.defaultPrevented){var c=a.target,d=a.submitter,e=c.action,b=d;if(d){var f=d.getAttribute(\"formAction\");null!=f&&(e=f,b=null)}\"javascript:throw new Error('React form unexpectedly submitted.')\"===e&&(a.preventDefault(),b?(a=document.createElement(\"input\"),a.name=b.name,a.value=b.value,b.parentNode.insertBefore(a,b),b=new FormData(c),a.parentNode.removeChild(a)):b=new FormData(c),a=c.ownerDocument||c,(a.$$reactFormReplay=a.$$reactFormReplay||[]).push(c,d,b))}});", "<\/script>");
		}
	}
	function pushLinkImpl(target, props) {
		target.push(startChunkForTag("link"));
		for (var propKey in props) if (hasOwnProperty.call(props, propKey)) {
			var propValue = props[propKey];
			if (null != propValue) switch (propKey) {
				case "children":
				case "dangerouslySetInnerHTML": throw Error(formatProdErrorMessage(399, "link"));
				default: pushAttribute(target, propKey, propValue);
			}
		}
		target.push("/>");
		return null;
	}
	var styleRegex = /(<\/|<)(s)(tyle)/gi;
	function styleReplacer(match, prefix, s, suffix) {
		return "" + prefix + ("s" === s ? "\\73 " : "\\53 ") + suffix;
	}
	function pushSelfClosing(target, props, tag) {
		target.push(startChunkForTag(tag));
		for (var propKey in props) if (hasOwnProperty.call(props, propKey)) {
			var propValue = props[propKey];
			if (null != propValue) switch (propKey) {
				case "children":
				case "dangerouslySetInnerHTML": throw Error(formatProdErrorMessage(399, tag));
				default: pushAttribute(target, propKey, propValue);
			}
		}
		target.push("/>");
		return null;
	}
	function pushTitleImpl(target, props) {
		target.push(startChunkForTag("title"));
		var children = null, innerHTML = null, propKey;
		for (propKey in props) if (hasOwnProperty.call(props, propKey)) {
			var propValue = props[propKey];
			if (null != propValue) switch (propKey) {
				case "children":
					children = propValue;
					break;
				case "dangerouslySetInnerHTML":
					innerHTML = propValue;
					break;
				default: pushAttribute(target, propKey, propValue);
			}
		}
		target.push(">");
		props = Array.isArray(children) ? 2 > children.length ? children[0] : null : children;
		"function" !== typeof props && "symbol" !== typeof props && null !== props && void 0 !== props && target.push(escapeTextForBrowser("" + props));
		pushInnerHTML(target, innerHTML, children);
		target.push(endChunkForTag("title"));
		return null;
	}
	function pushScriptImpl(target, props) {
		target.push(startChunkForTag("script"));
		var children = null, innerHTML = null, propKey;
		for (propKey in props) if (hasOwnProperty.call(props, propKey)) {
			var propValue = props[propKey];
			if (null != propValue) switch (propKey) {
				case "children":
					children = propValue;
					break;
				case "dangerouslySetInnerHTML":
					innerHTML = propValue;
					break;
				default: pushAttribute(target, propKey, propValue);
			}
		}
		target.push(">");
		pushInnerHTML(target, innerHTML, children);
		"string" === typeof children && target.push(("" + children).replace(scriptRegex, scriptReplacer));
		target.push(endChunkForTag("script"));
		return null;
	}
	function pushStartSingletonElement(target, props, tag) {
		target.push(startChunkForTag(tag));
		var innerHTML = tag = null, propKey;
		for (propKey in props) if (hasOwnProperty.call(props, propKey)) {
			var propValue = props[propKey];
			if (null != propValue) switch (propKey) {
				case "children":
					tag = propValue;
					break;
				case "dangerouslySetInnerHTML":
					innerHTML = propValue;
					break;
				default: pushAttribute(target, propKey, propValue);
			}
		}
		target.push(">");
		pushInnerHTML(target, innerHTML, tag);
		return tag;
	}
	function pushStartGenericElement(target, props, tag) {
		target.push(startChunkForTag(tag));
		var innerHTML = tag = null, propKey;
		for (propKey in props) if (hasOwnProperty.call(props, propKey)) {
			var propValue = props[propKey];
			if (null != propValue) switch (propKey) {
				case "children":
					tag = propValue;
					break;
				case "dangerouslySetInnerHTML":
					innerHTML = propValue;
					break;
				default: pushAttribute(target, propKey, propValue);
			}
		}
		target.push(">");
		pushInnerHTML(target, innerHTML, tag);
		return "string" === typeof tag ? (target.push(escapeTextForBrowser(tag)), null) : tag;
	}
	var VALID_TAG_REGEX = /^[a-zA-Z][a-zA-Z:_\.\-\d]*$/;
	var validatedTagCache = /* @__PURE__ */ new Map();
	function startChunkForTag(tag) {
		var tagStartChunk = validatedTagCache.get(tag);
		if (void 0 === tagStartChunk) {
			if (!VALID_TAG_REGEX.test(tag)) throw Error(formatProdErrorMessage(65, tag));
			tagStartChunk = "<" + tag;
			validatedTagCache.set(tag, tagStartChunk);
		}
		return tagStartChunk;
	}
	function pushStartInstance(target$jscomp$0, type, props, resumableState, renderState, preambleState, hoistableState, formatContext, textEmbedded) {
		switch (type) {
			case "div":
			case "span":
			case "svg":
			case "path": break;
			case "a":
				target$jscomp$0.push(startChunkForTag("a"));
				var children = null, innerHTML = null, propKey;
				for (propKey in props) if (hasOwnProperty.call(props, propKey)) {
					var propValue = props[propKey];
					if (null != propValue) switch (propKey) {
						case "children":
							children = propValue;
							break;
						case "dangerouslySetInnerHTML":
							innerHTML = propValue;
							break;
						case "href":
							"" === propValue ? pushStringAttribute(target$jscomp$0, "href", "") : pushAttribute(target$jscomp$0, propKey, propValue);
							break;
						default: pushAttribute(target$jscomp$0, propKey, propValue);
					}
				}
				target$jscomp$0.push(">");
				pushInnerHTML(target$jscomp$0, innerHTML, children);
				if ("string" === typeof children) {
					target$jscomp$0.push(escapeTextForBrowser(children));
					var JSCompiler_inline_result = null;
				} else JSCompiler_inline_result = children;
				return JSCompiler_inline_result;
			case "g":
			case "p":
			case "li": break;
			case "select":
				target$jscomp$0.push(startChunkForTag("select"));
				var children$jscomp$0 = null, innerHTML$jscomp$0 = null, propKey$jscomp$0;
				for (propKey$jscomp$0 in props) if (hasOwnProperty.call(props, propKey$jscomp$0)) {
					var propValue$jscomp$0 = props[propKey$jscomp$0];
					if (null != propValue$jscomp$0) switch (propKey$jscomp$0) {
						case "children":
							children$jscomp$0 = propValue$jscomp$0;
							break;
						case "dangerouslySetInnerHTML":
							innerHTML$jscomp$0 = propValue$jscomp$0;
							break;
						case "defaultValue":
						case "value": break;
						default: pushAttribute(target$jscomp$0, propKey$jscomp$0, propValue$jscomp$0);
					}
				}
				target$jscomp$0.push(">");
				pushInnerHTML(target$jscomp$0, innerHTML$jscomp$0, children$jscomp$0);
				return children$jscomp$0;
			case "option":
				var selectedValue = formatContext.selectedValue;
				target$jscomp$0.push(startChunkForTag("option"));
				var children$jscomp$1 = null, value = null, selected = null, innerHTML$jscomp$1 = null, propKey$jscomp$1;
				for (propKey$jscomp$1 in props) if (hasOwnProperty.call(props, propKey$jscomp$1)) {
					var propValue$jscomp$1 = props[propKey$jscomp$1];
					if (null != propValue$jscomp$1) switch (propKey$jscomp$1) {
						case "children":
							children$jscomp$1 = propValue$jscomp$1;
							break;
						case "selected":
							selected = propValue$jscomp$1;
							break;
						case "dangerouslySetInnerHTML":
							innerHTML$jscomp$1 = propValue$jscomp$1;
							break;
						case "value": value = propValue$jscomp$1;
						default: pushAttribute(target$jscomp$0, propKey$jscomp$1, propValue$jscomp$1);
					}
				}
				if (null != selectedValue) {
					var stringValue = null !== value ? "" + value : flattenOptionChildren(children$jscomp$1);
					if (isArrayImpl(selectedValue)) {
						for (var i = 0; i < selectedValue.length; i++) if ("" + selectedValue[i] === stringValue) {
							target$jscomp$0.push(" selected=\"\"");
							break;
						}
					} else "" + selectedValue === stringValue && target$jscomp$0.push(" selected=\"\"");
				} else selected && target$jscomp$0.push(" selected=\"\"");
				target$jscomp$0.push(">");
				pushInnerHTML(target$jscomp$0, innerHTML$jscomp$1, children$jscomp$1);
				return children$jscomp$1;
			case "textarea":
				target$jscomp$0.push(startChunkForTag("textarea"));
				var value$jscomp$0 = null, defaultValue = null, children$jscomp$2 = null, propKey$jscomp$2;
				for (propKey$jscomp$2 in props) if (hasOwnProperty.call(props, propKey$jscomp$2)) {
					var propValue$jscomp$2 = props[propKey$jscomp$2];
					if (null != propValue$jscomp$2) switch (propKey$jscomp$2) {
						case "children":
							children$jscomp$2 = propValue$jscomp$2;
							break;
						case "value":
							value$jscomp$0 = propValue$jscomp$2;
							break;
						case "defaultValue":
							defaultValue = propValue$jscomp$2;
							break;
						case "dangerouslySetInnerHTML": throw Error(formatProdErrorMessage(91));
						default: pushAttribute(target$jscomp$0, propKey$jscomp$2, propValue$jscomp$2);
					}
				}
				null === value$jscomp$0 && null !== defaultValue && (value$jscomp$0 = defaultValue);
				target$jscomp$0.push(">");
				if (null != children$jscomp$2) {
					if (null != value$jscomp$0) throw Error(formatProdErrorMessage(92));
					if (isArrayImpl(children$jscomp$2)) {
						if (1 < children$jscomp$2.length) throw Error(formatProdErrorMessage(93));
						value$jscomp$0 = "" + children$jscomp$2[0];
					}
					value$jscomp$0 = "" + children$jscomp$2;
				}
				"string" === typeof value$jscomp$0 && "\n" === value$jscomp$0[0] && target$jscomp$0.push("\n");
				null !== value$jscomp$0 && target$jscomp$0.push(escapeTextForBrowser("" + value$jscomp$0));
				return null;
			case "input":
				target$jscomp$0.push(startChunkForTag("input"));
				var name = null, formAction = null, formEncType = null, formMethod = null, formTarget = null, value$jscomp$1 = null, defaultValue$jscomp$0 = null, checked = null, defaultChecked = null, propKey$jscomp$3;
				for (propKey$jscomp$3 in props) if (hasOwnProperty.call(props, propKey$jscomp$3)) {
					var propValue$jscomp$3 = props[propKey$jscomp$3];
					if (null != propValue$jscomp$3) switch (propKey$jscomp$3) {
						case "children":
						case "dangerouslySetInnerHTML": throw Error(formatProdErrorMessage(399, "input"));
						case "name":
							name = propValue$jscomp$3;
							break;
						case "formAction":
							formAction = propValue$jscomp$3;
							break;
						case "formEncType":
							formEncType = propValue$jscomp$3;
							break;
						case "formMethod":
							formMethod = propValue$jscomp$3;
							break;
						case "formTarget":
							formTarget = propValue$jscomp$3;
							break;
						case "defaultChecked":
							defaultChecked = propValue$jscomp$3;
							break;
						case "defaultValue":
							defaultValue$jscomp$0 = propValue$jscomp$3;
							break;
						case "checked":
							checked = propValue$jscomp$3;
							break;
						case "value":
							value$jscomp$1 = propValue$jscomp$3;
							break;
						default: pushAttribute(target$jscomp$0, propKey$jscomp$3, propValue$jscomp$3);
					}
				}
				var formData = pushFormActionAttribute(target$jscomp$0, resumableState, renderState, formAction, formEncType, formMethod, formTarget, name);
				null !== checked ? pushBooleanAttribute(target$jscomp$0, "checked", checked) : null !== defaultChecked && pushBooleanAttribute(target$jscomp$0, "checked", defaultChecked);
				null !== value$jscomp$1 ? pushAttribute(target$jscomp$0, "value", value$jscomp$1) : null !== defaultValue$jscomp$0 && pushAttribute(target$jscomp$0, "value", defaultValue$jscomp$0);
				target$jscomp$0.push("/>");
				formData?.forEach(pushAdditionalFormField, target$jscomp$0);
				return null;
			case "button":
				target$jscomp$0.push(startChunkForTag("button"));
				var children$jscomp$3 = null, innerHTML$jscomp$2 = null, name$jscomp$0 = null, formAction$jscomp$0 = null, formEncType$jscomp$0 = null, formMethod$jscomp$0 = null, formTarget$jscomp$0 = null, propKey$jscomp$4;
				for (propKey$jscomp$4 in props) if (hasOwnProperty.call(props, propKey$jscomp$4)) {
					var propValue$jscomp$4 = props[propKey$jscomp$4];
					if (null != propValue$jscomp$4) switch (propKey$jscomp$4) {
						case "children":
							children$jscomp$3 = propValue$jscomp$4;
							break;
						case "dangerouslySetInnerHTML":
							innerHTML$jscomp$2 = propValue$jscomp$4;
							break;
						case "name":
							name$jscomp$0 = propValue$jscomp$4;
							break;
						case "formAction":
							formAction$jscomp$0 = propValue$jscomp$4;
							break;
						case "formEncType":
							formEncType$jscomp$0 = propValue$jscomp$4;
							break;
						case "formMethod":
							formMethod$jscomp$0 = propValue$jscomp$4;
							break;
						case "formTarget":
							formTarget$jscomp$0 = propValue$jscomp$4;
							break;
						default: pushAttribute(target$jscomp$0, propKey$jscomp$4, propValue$jscomp$4);
					}
				}
				var formData$jscomp$0 = pushFormActionAttribute(target$jscomp$0, resumableState, renderState, formAction$jscomp$0, formEncType$jscomp$0, formMethod$jscomp$0, formTarget$jscomp$0, name$jscomp$0);
				target$jscomp$0.push(">");
				formData$jscomp$0?.forEach(pushAdditionalFormField, target$jscomp$0);
				pushInnerHTML(target$jscomp$0, innerHTML$jscomp$2, children$jscomp$3);
				if ("string" === typeof children$jscomp$3) {
					target$jscomp$0.push(escapeTextForBrowser(children$jscomp$3));
					var JSCompiler_inline_result$jscomp$0 = null;
				} else JSCompiler_inline_result$jscomp$0 = children$jscomp$3;
				return JSCompiler_inline_result$jscomp$0;
			case "form":
				target$jscomp$0.push(startChunkForTag("form"));
				var children$jscomp$4 = null, innerHTML$jscomp$3 = null, formAction$jscomp$1 = null, formEncType$jscomp$1 = null, formMethod$jscomp$1 = null, formTarget$jscomp$1 = null, propKey$jscomp$5;
				for (propKey$jscomp$5 in props) if (hasOwnProperty.call(props, propKey$jscomp$5)) {
					var propValue$jscomp$5 = props[propKey$jscomp$5];
					if (null != propValue$jscomp$5) switch (propKey$jscomp$5) {
						case "children":
							children$jscomp$4 = propValue$jscomp$5;
							break;
						case "dangerouslySetInnerHTML":
							innerHTML$jscomp$3 = propValue$jscomp$5;
							break;
						case "action":
							formAction$jscomp$1 = propValue$jscomp$5;
							break;
						case "encType":
							formEncType$jscomp$1 = propValue$jscomp$5;
							break;
						case "method":
							formMethod$jscomp$1 = propValue$jscomp$5;
							break;
						case "target":
							formTarget$jscomp$1 = propValue$jscomp$5;
							break;
						default: pushAttribute(target$jscomp$0, propKey$jscomp$5, propValue$jscomp$5);
					}
				}
				var formData$jscomp$1 = null, formActionName = null;
				if ("function" === typeof formAction$jscomp$1) {
					var customFields = getCustomFormFields(resumableState, formAction$jscomp$1);
					null !== customFields ? (formAction$jscomp$1 = customFields.action || "", formEncType$jscomp$1 = customFields.encType, formMethod$jscomp$1 = customFields.method, formTarget$jscomp$1 = customFields.target, formData$jscomp$1 = customFields.data, formActionName = customFields.name) : (target$jscomp$0.push(" ", "action", "=\"", actionJavaScriptURL, "\""), formTarget$jscomp$1 = formMethod$jscomp$1 = formEncType$jscomp$1 = formAction$jscomp$1 = null, injectFormReplayingRuntime(resumableState, renderState));
				}
				null != formAction$jscomp$1 && pushAttribute(target$jscomp$0, "action", formAction$jscomp$1);
				null != formEncType$jscomp$1 && pushAttribute(target$jscomp$0, "encType", formEncType$jscomp$1);
				null != formMethod$jscomp$1 && pushAttribute(target$jscomp$0, "method", formMethod$jscomp$1);
				null != formTarget$jscomp$1 && pushAttribute(target$jscomp$0, "target", formTarget$jscomp$1);
				target$jscomp$0.push(">");
				null !== formActionName && (target$jscomp$0.push("<input type=\"hidden\""), pushStringAttribute(target$jscomp$0, "name", formActionName), target$jscomp$0.push("/>"), formData$jscomp$1?.forEach(pushAdditionalFormField, target$jscomp$0));
				pushInnerHTML(target$jscomp$0, innerHTML$jscomp$3, children$jscomp$4);
				if ("string" === typeof children$jscomp$4) {
					target$jscomp$0.push(escapeTextForBrowser(children$jscomp$4));
					var JSCompiler_inline_result$jscomp$1 = null;
				} else JSCompiler_inline_result$jscomp$1 = children$jscomp$4;
				return JSCompiler_inline_result$jscomp$1;
			case "menuitem":
				target$jscomp$0.push(startChunkForTag("menuitem"));
				for (var propKey$jscomp$6 in props) if (hasOwnProperty.call(props, propKey$jscomp$6)) {
					var propValue$jscomp$6 = props[propKey$jscomp$6];
					if (null != propValue$jscomp$6) switch (propKey$jscomp$6) {
						case "children":
						case "dangerouslySetInnerHTML": throw Error(formatProdErrorMessage(400));
						default: pushAttribute(target$jscomp$0, propKey$jscomp$6, propValue$jscomp$6);
					}
				}
				target$jscomp$0.push(">");
				return null;
			case "object":
				target$jscomp$0.push(startChunkForTag("object"));
				var children$jscomp$5 = null, innerHTML$jscomp$4 = null, propKey$jscomp$7;
				for (propKey$jscomp$7 in props) if (hasOwnProperty.call(props, propKey$jscomp$7)) {
					var propValue$jscomp$7 = props[propKey$jscomp$7];
					if (null != propValue$jscomp$7) switch (propKey$jscomp$7) {
						case "children":
							children$jscomp$5 = propValue$jscomp$7;
							break;
						case "dangerouslySetInnerHTML":
							innerHTML$jscomp$4 = propValue$jscomp$7;
							break;
						case "data":
							var sanitizedValue = sanitizeURL("" + propValue$jscomp$7);
							if ("" === sanitizedValue) break;
							target$jscomp$0.push(" ", "data", "=\"", escapeTextForBrowser(sanitizedValue), "\"");
							break;
						default: pushAttribute(target$jscomp$0, propKey$jscomp$7, propValue$jscomp$7);
					}
				}
				target$jscomp$0.push(">");
				pushInnerHTML(target$jscomp$0, innerHTML$jscomp$4, children$jscomp$5);
				if ("string" === typeof children$jscomp$5) {
					target$jscomp$0.push(escapeTextForBrowser(children$jscomp$5));
					var JSCompiler_inline_result$jscomp$2 = null;
				} else JSCompiler_inline_result$jscomp$2 = children$jscomp$5;
				return JSCompiler_inline_result$jscomp$2;
			case "title":
				var noscriptTagInScope = formatContext.tagScope & 1, isFallback = formatContext.tagScope & 4;
				if (4 === formatContext.insertionMode || noscriptTagInScope || null != props.itemProp) var JSCompiler_inline_result$jscomp$3 = pushTitleImpl(target$jscomp$0, props);
				else isFallback ? JSCompiler_inline_result$jscomp$3 = null : (pushTitleImpl(renderState.hoistableChunks, props), JSCompiler_inline_result$jscomp$3 = void 0);
				return JSCompiler_inline_result$jscomp$3;
			case "link":
				var noscriptTagInScope$jscomp$0 = formatContext.tagScope & 1, isFallback$jscomp$0 = formatContext.tagScope & 4, rel = props.rel, href = props.href, precedence = props.precedence;
				if (4 === formatContext.insertionMode || noscriptTagInScope$jscomp$0 || null != props.itemProp || "string" !== typeof rel || "string" !== typeof href || "" === href) {
					pushLinkImpl(target$jscomp$0, props);
					var JSCompiler_inline_result$jscomp$4 = null;
				} else if ("stylesheet" === props.rel) if ("string" !== typeof precedence || null != props.disabled || props.onLoad || props.onError) JSCompiler_inline_result$jscomp$4 = pushLinkImpl(target$jscomp$0, props);
				else {
					var styleQueue = renderState.styles.get(precedence), resourceState = resumableState.styleResources.hasOwnProperty(href) ? resumableState.styleResources[href] : void 0;
					if (null !== resourceState) {
						resumableState.styleResources[href] = null;
						styleQueue || (styleQueue = {
							precedence: escapeTextForBrowser(precedence),
							rules: [],
							hrefs: [],
							sheets: /* @__PURE__ */ new Map()
						}, renderState.styles.set(precedence, styleQueue));
						var resource = {
							state: 0,
							props: assign({}, props, {
								"data-precedence": props.precedence,
								precedence: null
							})
						};
						if (resourceState) {
							2 === resourceState.length && adoptPreloadCredentials(resource.props, resourceState);
							var preloadResource = renderState.preloads.stylesheets.get(href);
							preloadResource && 0 < preloadResource.length ? preloadResource.length = 0 : resource.state = 1;
						}
						styleQueue.sheets.set(href, resource);
						hoistableState && hoistableState.stylesheets.add(resource);
					} else if (styleQueue) {
						var resource$9 = styleQueue.sheets.get(href);
						resource$9 && hoistableState && hoistableState.stylesheets.add(resource$9);
					}
					textEmbedded && target$jscomp$0.push("<!-- -->");
					JSCompiler_inline_result$jscomp$4 = null;
				}
				else props.onLoad || props.onError ? JSCompiler_inline_result$jscomp$4 = pushLinkImpl(target$jscomp$0, props) : (textEmbedded && target$jscomp$0.push("<!-- -->"), JSCompiler_inline_result$jscomp$4 = isFallback$jscomp$0 ? null : pushLinkImpl(renderState.hoistableChunks, props));
				return JSCompiler_inline_result$jscomp$4;
			case "script":
				var noscriptTagInScope$jscomp$1 = formatContext.tagScope & 1, asyncProp = props.async;
				if ("string" !== typeof props.src || !props.src || !asyncProp || "function" === typeof asyncProp || "symbol" === typeof asyncProp || props.onLoad || props.onError || 4 === formatContext.insertionMode || noscriptTagInScope$jscomp$1 || null != props.itemProp) var JSCompiler_inline_result$jscomp$5 = pushScriptImpl(target$jscomp$0, props);
				else {
					var key = props.src;
					if ("module" === props.type) {
						var resources = resumableState.moduleScriptResources;
						var preloads = renderState.preloads.moduleScripts;
					} else resources = resumableState.scriptResources, preloads = renderState.preloads.scripts;
					var resourceState$jscomp$0 = resources.hasOwnProperty(key) ? resources[key] : void 0;
					if (null !== resourceState$jscomp$0) {
						resources[key] = null;
						var scriptProps = props;
						if (resourceState$jscomp$0) {
							2 === resourceState$jscomp$0.length && (scriptProps = assign({}, props), adoptPreloadCredentials(scriptProps, resourceState$jscomp$0));
							var preloadResource$jscomp$0 = preloads.get(key);
							preloadResource$jscomp$0 && (preloadResource$jscomp$0.length = 0);
						}
						var resource$jscomp$0 = [];
						renderState.scripts.add(resource$jscomp$0);
						pushScriptImpl(resource$jscomp$0, scriptProps);
					}
					textEmbedded && target$jscomp$0.push("<!-- -->");
					JSCompiler_inline_result$jscomp$5 = null;
				}
				return JSCompiler_inline_result$jscomp$5;
			case "style":
				var noscriptTagInScope$jscomp$2 = formatContext.tagScope & 1, precedence$jscomp$0 = props.precedence, href$jscomp$0 = props.href, nonce = props.nonce;
				if (4 === formatContext.insertionMode || noscriptTagInScope$jscomp$2 || null != props.itemProp || "string" !== typeof precedence$jscomp$0 || "string" !== typeof href$jscomp$0 || "" === href$jscomp$0) {
					target$jscomp$0.push(startChunkForTag("style"));
					var children$jscomp$6 = null, innerHTML$jscomp$5 = null, propKey$jscomp$8;
					for (propKey$jscomp$8 in props) if (hasOwnProperty.call(props, propKey$jscomp$8)) {
						var propValue$jscomp$8 = props[propKey$jscomp$8];
						if (null != propValue$jscomp$8) switch (propKey$jscomp$8) {
							case "children":
								children$jscomp$6 = propValue$jscomp$8;
								break;
							case "dangerouslySetInnerHTML":
								innerHTML$jscomp$5 = propValue$jscomp$8;
								break;
							default: pushAttribute(target$jscomp$0, propKey$jscomp$8, propValue$jscomp$8);
						}
					}
					target$jscomp$0.push(">");
					var child = Array.isArray(children$jscomp$6) ? 2 > children$jscomp$6.length ? children$jscomp$6[0] : null : children$jscomp$6;
					"function" !== typeof child && "symbol" !== typeof child && null !== child && void 0 !== child && target$jscomp$0.push(("" + child).replace(styleRegex, styleReplacer));
					pushInnerHTML(target$jscomp$0, innerHTML$jscomp$5, children$jscomp$6);
					target$jscomp$0.push(endChunkForTag("style"));
					var JSCompiler_inline_result$jscomp$6 = null;
				} else {
					var styleQueue$jscomp$0 = renderState.styles.get(precedence$jscomp$0);
					if (null !== (resumableState.styleResources.hasOwnProperty(href$jscomp$0) ? resumableState.styleResources[href$jscomp$0] : void 0)) {
						resumableState.styleResources[href$jscomp$0] = null;
						styleQueue$jscomp$0 || (styleQueue$jscomp$0 = {
							precedence: escapeTextForBrowser(precedence$jscomp$0),
							rules: [],
							hrefs: [],
							sheets: /* @__PURE__ */ new Map()
						}, renderState.styles.set(precedence$jscomp$0, styleQueue$jscomp$0));
						var nonceStyle = renderState.nonce.style;
						if (!nonceStyle || nonceStyle === nonce) {
							styleQueue$jscomp$0.hrefs.push(escapeTextForBrowser(href$jscomp$0));
							var target = styleQueue$jscomp$0.rules, children$jscomp$7 = null, innerHTML$jscomp$6 = null, propKey$jscomp$9;
							for (propKey$jscomp$9 in props) if (hasOwnProperty.call(props, propKey$jscomp$9)) {
								var propValue$jscomp$9 = props[propKey$jscomp$9];
								if (null != propValue$jscomp$9) switch (propKey$jscomp$9) {
									case "children":
										children$jscomp$7 = propValue$jscomp$9;
										break;
									case "dangerouslySetInnerHTML": innerHTML$jscomp$6 = propValue$jscomp$9;
								}
							}
							var child$jscomp$0 = Array.isArray(children$jscomp$7) ? 2 > children$jscomp$7.length ? children$jscomp$7[0] : null : children$jscomp$7;
							"function" !== typeof child$jscomp$0 && "symbol" !== typeof child$jscomp$0 && null !== child$jscomp$0 && void 0 !== child$jscomp$0 && target.push(("" + child$jscomp$0).replace(styleRegex, styleReplacer));
							pushInnerHTML(target, innerHTML$jscomp$6, children$jscomp$7);
						}
					}
					styleQueue$jscomp$0 && hoistableState && hoistableState.styles.add(styleQueue$jscomp$0);
					textEmbedded && target$jscomp$0.push("<!-- -->");
					JSCompiler_inline_result$jscomp$6 = void 0;
				}
				return JSCompiler_inline_result$jscomp$6;
			case "meta":
				var noscriptTagInScope$jscomp$3 = formatContext.tagScope & 1, isFallback$jscomp$1 = formatContext.tagScope & 4;
				if (4 === formatContext.insertionMode || noscriptTagInScope$jscomp$3 || null != props.itemProp) var JSCompiler_inline_result$jscomp$7 = pushSelfClosing(target$jscomp$0, props, "meta");
				else textEmbedded && target$jscomp$0.push("<!-- -->"), JSCompiler_inline_result$jscomp$7 = isFallback$jscomp$1 ? null : "string" === typeof props.charSet ? pushSelfClosing(renderState.charsetChunks, props, "meta") : "viewport" === props.name ? pushSelfClosing(renderState.viewportChunks, props, "meta") : pushSelfClosing(renderState.hoistableChunks, props, "meta");
				return JSCompiler_inline_result$jscomp$7;
			case "listing":
			case "pre":
				target$jscomp$0.push(startChunkForTag(type));
				var children$jscomp$8 = null, innerHTML$jscomp$7 = null, propKey$jscomp$10;
				for (propKey$jscomp$10 in props) if (hasOwnProperty.call(props, propKey$jscomp$10)) {
					var propValue$jscomp$10 = props[propKey$jscomp$10];
					if (null != propValue$jscomp$10) switch (propKey$jscomp$10) {
						case "children":
							children$jscomp$8 = propValue$jscomp$10;
							break;
						case "dangerouslySetInnerHTML":
							innerHTML$jscomp$7 = propValue$jscomp$10;
							break;
						default: pushAttribute(target$jscomp$0, propKey$jscomp$10, propValue$jscomp$10);
					}
				}
				target$jscomp$0.push(">");
				if (null != innerHTML$jscomp$7) {
					if (null != children$jscomp$8) throw Error(formatProdErrorMessage(60));
					if ("object" !== typeof innerHTML$jscomp$7 || !("__html" in innerHTML$jscomp$7)) throw Error(formatProdErrorMessage(61));
					var html = innerHTML$jscomp$7.__html;
					null !== html && void 0 !== html && ("string" === typeof html && 0 < html.length && "\n" === html[0] ? target$jscomp$0.push("\n", html) : target$jscomp$0.push("" + html));
				}
				"string" === typeof children$jscomp$8 && "\n" === children$jscomp$8[0] && target$jscomp$0.push("\n");
				return children$jscomp$8;
			case "img":
				var pictureOrNoScriptTagInScope = formatContext.tagScope & 3, src = props.src, srcSet = props.srcSet;
				if (!("lazy" === props.loading || !src && !srcSet || "string" !== typeof src && null != src || "string" !== typeof srcSet && null != srcSet || "low" === props.fetchPriority || pictureOrNoScriptTagInScope) && ("string" !== typeof src || ":" !== src[4] || "d" !== src[0] && "D" !== src[0] || "a" !== src[1] && "A" !== src[1] || "t" !== src[2] && "T" !== src[2] || "a" !== src[3] && "A" !== src[3]) && ("string" !== typeof srcSet || ":" !== srcSet[4] || "d" !== srcSet[0] && "D" !== srcSet[0] || "a" !== srcSet[1] && "A" !== srcSet[1] || "t" !== srcSet[2] && "T" !== srcSet[2] || "a" !== srcSet[3] && "A" !== srcSet[3])) {
					null !== hoistableState && formatContext.tagScope & 64 && (hoistableState.suspenseyImages = !0);
					var sizes = "string" === typeof props.sizes ? props.sizes : void 0, key$jscomp$0 = srcSet ? srcSet + "\n" + (sizes || "") : src, promotablePreloads = renderState.preloads.images, resource$jscomp$1 = promotablePreloads.get(key$jscomp$0);
					if (resource$jscomp$1) {
						if ("high" === props.fetchPriority || 10 > renderState.highImagePreloads.size) promotablePreloads.delete(key$jscomp$0), renderState.highImagePreloads.add(resource$jscomp$1);
					} else if (!resumableState.imageResources.hasOwnProperty(key$jscomp$0)) {
						resumableState.imageResources[key$jscomp$0] = PRELOAD_NO_CREDS;
						var input = props.crossOrigin;
						var JSCompiler_inline_result$jscomp$8 = "string" === typeof input ? "use-credentials" === input ? input : "" : void 0;
						var headers = renderState.headers, header;
						headers && 0 < headers.remainingCapacity && "string" !== typeof props.srcSet && ("high" === props.fetchPriority || 500 > headers.highImagePreloads.length) && (header = getPreloadAsHeader(src, "image", {
							imageSrcSet: props.srcSet,
							imageSizes: props.sizes,
							crossOrigin: JSCompiler_inline_result$jscomp$8,
							integrity: props.integrity,
							nonce: props.nonce,
							type: props.type,
							fetchPriority: props.fetchPriority,
							referrerPolicy: props.refererPolicy
						}), 0 <= (headers.remainingCapacity -= header.length + 2)) ? (renderState.resets.image[key$jscomp$0] = PRELOAD_NO_CREDS, headers.highImagePreloads && (headers.highImagePreloads += ", "), headers.highImagePreloads += header) : (resource$jscomp$1 = [], pushLinkImpl(resource$jscomp$1, {
							rel: "preload",
							as: "image",
							href: srcSet ? void 0 : src,
							imageSrcSet: srcSet,
							imageSizes: sizes,
							crossOrigin: JSCompiler_inline_result$jscomp$8,
							integrity: props.integrity,
							type: props.type,
							fetchPriority: props.fetchPriority,
							referrerPolicy: props.referrerPolicy
						}), "high" === props.fetchPriority || 10 > renderState.highImagePreloads.size ? renderState.highImagePreloads.add(resource$jscomp$1) : (renderState.bulkPreloads.add(resource$jscomp$1), promotablePreloads.set(key$jscomp$0, resource$jscomp$1)));
					}
				}
				return pushSelfClosing(target$jscomp$0, props, "img");
			case "base":
			case "area":
			case "br":
			case "col":
			case "embed":
			case "hr":
			case "keygen":
			case "param":
			case "source":
			case "track":
			case "wbr": return pushSelfClosing(target$jscomp$0, props, type);
			case "annotation-xml":
			case "color-profile":
			case "font-face":
			case "font-face-src":
			case "font-face-uri":
			case "font-face-format":
			case "font-face-name":
			case "missing-glyph": break;
			case "head":
				if (2 > formatContext.insertionMode) {
					var preamble = preambleState || renderState.preamble;
					if (preamble.headChunks) throw Error(formatProdErrorMessage(545, "`<head>`"));
					null !== preambleState && target$jscomp$0.push("<!--head-->");
					preamble.headChunks = [];
					var JSCompiler_inline_result$jscomp$9 = pushStartSingletonElement(preamble.headChunks, props, "head");
				} else JSCompiler_inline_result$jscomp$9 = pushStartGenericElement(target$jscomp$0, props, "head");
				return JSCompiler_inline_result$jscomp$9;
			case "body":
				if (2 > formatContext.insertionMode) {
					var preamble$jscomp$0 = preambleState || renderState.preamble;
					if (preamble$jscomp$0.bodyChunks) throw Error(formatProdErrorMessage(545, "`<body>`"));
					null !== preambleState && target$jscomp$0.push("<!--body-->");
					preamble$jscomp$0.bodyChunks = [];
					var JSCompiler_inline_result$jscomp$10 = pushStartSingletonElement(preamble$jscomp$0.bodyChunks, props, "body");
				} else JSCompiler_inline_result$jscomp$10 = pushStartGenericElement(target$jscomp$0, props, "body");
				return JSCompiler_inline_result$jscomp$10;
			case "html":
				if (0 === formatContext.insertionMode) {
					var preamble$jscomp$1 = preambleState || renderState.preamble;
					if (preamble$jscomp$1.htmlChunks) throw Error(formatProdErrorMessage(545, "`<html>`"));
					null !== preambleState && target$jscomp$0.push("<!--html-->");
					preamble$jscomp$1.htmlChunks = [""];
					var JSCompiler_inline_result$jscomp$11 = pushStartSingletonElement(preamble$jscomp$1.htmlChunks, props, "html");
				} else JSCompiler_inline_result$jscomp$11 = pushStartGenericElement(target$jscomp$0, props, "html");
				return JSCompiler_inline_result$jscomp$11;
			default: if (-1 !== type.indexOf("-")) {
				target$jscomp$0.push(startChunkForTag(type));
				var children$jscomp$9 = null, innerHTML$jscomp$8 = null, propKey$jscomp$11;
				for (propKey$jscomp$11 in props) if (hasOwnProperty.call(props, propKey$jscomp$11)) {
					var propValue$jscomp$11 = props[propKey$jscomp$11];
					if (null != propValue$jscomp$11) {
						var attributeName = propKey$jscomp$11;
						switch (propKey$jscomp$11) {
							case "children":
								children$jscomp$9 = propValue$jscomp$11;
								break;
							case "dangerouslySetInnerHTML":
								innerHTML$jscomp$8 = propValue$jscomp$11;
								break;
							case "style":
								pushStyleAttribute(target$jscomp$0, propValue$jscomp$11);
								break;
							case "suppressContentEditableWarning":
							case "suppressHydrationWarning":
							case "ref": break;
							case "className": attributeName = "class";
							default: if (isAttributeNameSafe(propKey$jscomp$11) && "function" !== typeof propValue$jscomp$11 && "symbol" !== typeof propValue$jscomp$11 && !1 !== propValue$jscomp$11) {
								if (!0 === propValue$jscomp$11) propValue$jscomp$11 = "";
								else if ("object" === typeof propValue$jscomp$11) continue;
								target$jscomp$0.push(" ", attributeName, "=\"", escapeTextForBrowser(propValue$jscomp$11), "\"");
							}
						}
					}
				}
				target$jscomp$0.push(">");
				pushInnerHTML(target$jscomp$0, innerHTML$jscomp$8, children$jscomp$9);
				return children$jscomp$9;
			}
		}
		return pushStartGenericElement(target$jscomp$0, props, type);
	}
	var endTagCache = /* @__PURE__ */ new Map();
	function endChunkForTag(tag) {
		var chunk = endTagCache.get(tag);
		void 0 === chunk && (chunk = "</" + tag + ">", endTagCache.set(tag, chunk));
		return chunk;
	}
	function hoistPreambleState(renderState, preambleState) {
		renderState = renderState.preamble;
		null === renderState.htmlChunks && preambleState.htmlChunks && (renderState.htmlChunks = preambleState.htmlChunks);
		null === renderState.headChunks && preambleState.headChunks && (renderState.headChunks = preambleState.headChunks);
		null === renderState.bodyChunks && preambleState.bodyChunks && (renderState.bodyChunks = preambleState.bodyChunks);
	}
	function writeBootstrap(destination, renderState) {
		renderState = renderState.bootstrapChunks;
		for (var i = 0; i < renderState.length - 1; i++) destination.push(renderState[i]);
		return i < renderState.length ? (i = renderState[i], renderState.length = 0, destination.push(i)) : !0;
	}
	function writeStartPendingSuspenseBoundary(destination, renderState, id) {
		destination.push("<!--$?--><template id=\"");
		if (null === id) throw Error(formatProdErrorMessage(395));
		destination.push(renderState.boundaryPrefix);
		renderState = id.toString(16);
		destination.push(renderState);
		return destination.push("\"></template>");
	}
	function writeStartSegment(destination, renderState, formatContext, id) {
		switch (formatContext.insertionMode) {
			case 0:
			case 1:
			case 3:
			case 2: return destination.push("<div hidden id=\""), destination.push(renderState.segmentPrefix), renderState = id.toString(16), destination.push(renderState), destination.push("\">");
			case 4: return destination.push("<svg aria-hidden=\"true\" style=\"display:none\" id=\""), destination.push(renderState.segmentPrefix), renderState = id.toString(16), destination.push(renderState), destination.push("\">");
			case 5: return destination.push("<math aria-hidden=\"true\" style=\"display:none\" id=\""), destination.push(renderState.segmentPrefix), renderState = id.toString(16), destination.push(renderState), destination.push("\">");
			case 6: return destination.push("<table hidden id=\""), destination.push(renderState.segmentPrefix), renderState = id.toString(16), destination.push(renderState), destination.push("\">");
			case 7: return destination.push("<table hidden><tbody id=\""), destination.push(renderState.segmentPrefix), renderState = id.toString(16), destination.push(renderState), destination.push("\">");
			case 8: return destination.push("<table hidden><tr id=\""), destination.push(renderState.segmentPrefix), renderState = id.toString(16), destination.push(renderState), destination.push("\">");
			case 9: return destination.push("<table hidden><colgroup id=\""), destination.push(renderState.segmentPrefix), renderState = id.toString(16), destination.push(renderState), destination.push("\">");
			default: throw Error(formatProdErrorMessage(397));
		}
	}
	function writeEndSegment(destination, formatContext) {
		switch (formatContext.insertionMode) {
			case 0:
			case 1:
			case 3:
			case 2: return destination.push("</div>");
			case 4: return destination.push("</svg>");
			case 5: return destination.push("</math>");
			case 6: return destination.push("</table>");
			case 7: return destination.push("</tbody></table>");
			case 8: return destination.push("</tr></table>");
			case 9: return destination.push("</colgroup></table>");
			default: throw Error(formatProdErrorMessage(397));
		}
	}
	var regexForJSStringsInInstructionScripts = /[<\u2028\u2029]/g;
	function escapeJSStringsForInstructionScripts(input) {
		return JSON.stringify(input).replace(regexForJSStringsInInstructionScripts, function(match) {
			switch (match) {
				case "<": return "\\u003c";
				case "\u2028": return "\\u2028";
				case "\u2029": return "\\u2029";
				default: throw Error("escapeJSStringsForInstructionScripts encountered a match it does not know how to replace. this means the match regex and the replacement characters are no longer in sync. This is a bug in React");
			}
		});
	}
	var regexForJSStringsInScripts = /[&><\u2028\u2029]/g;
	function escapeJSObjectForInstructionScripts(input) {
		return JSON.stringify(input).replace(regexForJSStringsInScripts, function(match) {
			switch (match) {
				case "&": return "\\u0026";
				case ">": return "\\u003e";
				case "<": return "\\u003c";
				case "\u2028": return "\\u2028";
				case "\u2029": return "\\u2029";
				default: throw Error("escapeJSObjectForInstructionScripts encountered a match it does not know how to replace. this means the match regex and the replacement characters are no longer in sync. This is a bug in React");
			}
		});
	}
	var currentlyRenderingBoundaryHasStylesToHoist = !1;
	var destinationHasCapacity = !0;
	function flushStyleTagsLateForBoundary(styleQueue) {
		var rules = styleQueue.rules, hrefs = styleQueue.hrefs, i = 0;
		if (hrefs.length) {
			this.push(currentlyFlushingRenderState.startInlineStyle);
			this.push(" media=\"not all\" data-precedence=\"");
			this.push(styleQueue.precedence);
			for (this.push("\" data-href=\""); i < hrefs.length - 1; i++) this.push(hrefs[i]), this.push(" ");
			this.push(hrefs[i]);
			this.push("\">");
			for (i = 0; i < rules.length; i++) this.push(rules[i]);
			destinationHasCapacity = this.push("</style>");
			currentlyRenderingBoundaryHasStylesToHoist = !0;
			rules.length = 0;
			hrefs.length = 0;
		}
	}
	function hasStylesToHoist(stylesheet) {
		return 2 !== stylesheet.state ? currentlyRenderingBoundaryHasStylesToHoist = !0 : !1;
	}
	function writeHoistablesForBoundary(destination, hoistableState, renderState) {
		currentlyRenderingBoundaryHasStylesToHoist = !1;
		destinationHasCapacity = !0;
		currentlyFlushingRenderState = renderState;
		hoistableState.styles.forEach(flushStyleTagsLateForBoundary, destination);
		currentlyFlushingRenderState = null;
		hoistableState.stylesheets.forEach(hasStylesToHoist);
		currentlyRenderingBoundaryHasStylesToHoist && (renderState.stylesToHoist = !0);
		return destinationHasCapacity;
	}
	function flushResource(resource) {
		for (var i = 0; i < resource.length; i++) this.push(resource[i]);
		resource.length = 0;
	}
	var stylesheetFlushingQueue = [];
	function flushStyleInPreamble(stylesheet) {
		pushLinkImpl(stylesheetFlushingQueue, stylesheet.props);
		for (var i = 0; i < stylesheetFlushingQueue.length; i++) this.push(stylesheetFlushingQueue[i]);
		stylesheetFlushingQueue.length = 0;
		stylesheet.state = 2;
	}
	function flushStylesInPreamble(styleQueue) {
		var hasStylesheets = 0 < styleQueue.sheets.size;
		styleQueue.sheets.forEach(flushStyleInPreamble, this);
		styleQueue.sheets.clear();
		var rules = styleQueue.rules, hrefs = styleQueue.hrefs;
		if (!hasStylesheets || hrefs.length) {
			this.push(currentlyFlushingRenderState.startInlineStyle);
			this.push(" data-precedence=\"");
			this.push(styleQueue.precedence);
			styleQueue = 0;
			if (hrefs.length) {
				for (this.push("\" data-href=\""); styleQueue < hrefs.length - 1; styleQueue++) this.push(hrefs[styleQueue]), this.push(" ");
				this.push(hrefs[styleQueue]);
			}
			this.push("\">");
			for (styleQueue = 0; styleQueue < rules.length; styleQueue++) this.push(rules[styleQueue]);
			this.push("</style>");
			rules.length = 0;
			hrefs.length = 0;
		}
	}
	function preloadLateStyle(stylesheet) {
		if (0 === stylesheet.state) {
			stylesheet.state = 1;
			var props = stylesheet.props;
			pushLinkImpl(stylesheetFlushingQueue, {
				rel: "preload",
				as: "style",
				href: stylesheet.props.href,
				crossOrigin: props.crossOrigin,
				fetchPriority: props.fetchPriority,
				integrity: props.integrity,
				media: props.media,
				hrefLang: props.hrefLang,
				referrerPolicy: props.referrerPolicy
			});
			for (stylesheet = 0; stylesheet < stylesheetFlushingQueue.length; stylesheet++) this.push(stylesheetFlushingQueue[stylesheet]);
			stylesheetFlushingQueue.length = 0;
		}
	}
	function preloadLateStyles(styleQueue) {
		styleQueue.sheets.forEach(preloadLateStyle, this);
		styleQueue.sheets.clear();
	}
	function pushCompletedShellIdAttribute(target, resumableState) {
		0 === (resumableState.instructions & 32) && (resumableState.instructions |= 32, target.push(" id=\"", escapeTextForBrowser("_" + resumableState.idPrefix + "R_"), "\""));
	}
	function writeStyleResourceDependenciesInJS(destination, hoistableState) {
		destination.push("[");
		var nextArrayOpenBrackChunk = "[";
		hoistableState.stylesheets.forEach(function(resource) {
			if (2 !== resource.state) if (3 === resource.state) destination.push(nextArrayOpenBrackChunk), resource = escapeJSObjectForInstructionScripts("" + resource.props.href), destination.push(resource), destination.push("]"), nextArrayOpenBrackChunk = ",[";
			else {
				destination.push(nextArrayOpenBrackChunk);
				var precedence = resource.props["data-precedence"], props = resource.props, coercedHref = sanitizeURL("" + resource.props.href);
				coercedHref = escapeJSObjectForInstructionScripts(coercedHref);
				destination.push(coercedHref);
				precedence = "" + precedence;
				destination.push(",");
				precedence = escapeJSObjectForInstructionScripts(precedence);
				destination.push(precedence);
				for (var propKey in props) if (hasOwnProperty.call(props, propKey) && (precedence = props[propKey], null != precedence)) switch (propKey) {
					case "href":
					case "rel":
					case "precedence":
					case "data-precedence": break;
					case "children":
					case "dangerouslySetInnerHTML": throw Error(formatProdErrorMessage(399, "link"));
					default: writeStyleResourceAttributeInJS(destination, propKey, precedence);
				}
				destination.push("]");
				nextArrayOpenBrackChunk = ",[";
				resource.state = 3;
			}
		});
		destination.push("]");
	}
	function writeStyleResourceAttributeInJS(destination, name, value) {
		var attributeName = name.toLowerCase();
		switch (typeof value) {
			case "function":
			case "symbol": return;
		}
		switch (name) {
			case "innerHTML":
			case "dangerouslySetInnerHTML":
			case "suppressContentEditableWarning":
			case "suppressHydrationWarning":
			case "style":
			case "ref": return;
			case "className":
				attributeName = "class";
				name = "" + value;
				break;
			case "hidden":
				if (!1 === value) return;
				name = "";
				break;
			case "src":
			case "href":
				value = sanitizeURL(value);
				name = "" + value;
				break;
			default:
				if (2 < name.length && ("o" === name[0] || "O" === name[0]) && ("n" === name[1] || "N" === name[1]) || !isAttributeNameSafe(name)) return;
				name = "" + value;
		}
		destination.push(",");
		attributeName = escapeJSObjectForInstructionScripts(attributeName);
		destination.push(attributeName);
		destination.push(",");
		attributeName = escapeJSObjectForInstructionScripts(name);
		destination.push(attributeName);
	}
	function createHoistableState() {
		return {
			styles: /* @__PURE__ */ new Set(),
			stylesheets: /* @__PURE__ */ new Set(),
			suspenseyImages: !1
		};
	}
	function prefetchDNS(href) {
		var request = currentRequest ? currentRequest : null;
		if (request) {
			var resumableState = request.resumableState, renderState = request.renderState;
			if ("string" === typeof href && href) {
				if (!resumableState.dnsResources.hasOwnProperty(href)) {
					resumableState.dnsResources[href] = null;
					resumableState = renderState.headers;
					var header, JSCompiler_temp;
					if (JSCompiler_temp = resumableState && 0 < resumableState.remainingCapacity) JSCompiler_temp = (header = "<" + ("" + href).replace(regexForHrefInLinkHeaderURLContext, escapeHrefForLinkHeaderURLContextReplacer) + ">; rel=dns-prefetch", 0 <= (resumableState.remainingCapacity -= header.length + 2));
					JSCompiler_temp ? (renderState.resets.dns[href] = null, resumableState.preconnects && (resumableState.preconnects += ", "), resumableState.preconnects += header) : (header = [], pushLinkImpl(header, {
						href,
						rel: "dns-prefetch"
					}), renderState.preconnects.add(header));
				}
				enqueueFlush(request);
			}
		} else previousDispatcher.D(href);
	}
	function preconnect(href, crossOrigin) {
		var request = currentRequest ? currentRequest : null;
		if (request) {
			var resumableState = request.resumableState, renderState = request.renderState;
			if ("string" === typeof href && href) {
				var bucket = "use-credentials" === crossOrigin ? "credentials" : "string" === typeof crossOrigin ? "anonymous" : "default";
				if (!resumableState.connectResources[bucket].hasOwnProperty(href)) {
					resumableState.connectResources[bucket][href] = null;
					resumableState = renderState.headers;
					var header, JSCompiler_temp;
					if (JSCompiler_temp = resumableState && 0 < resumableState.remainingCapacity) {
						JSCompiler_temp = "<" + ("" + href).replace(regexForHrefInLinkHeaderURLContext, escapeHrefForLinkHeaderURLContextReplacer) + ">; rel=preconnect";
						if ("string" === typeof crossOrigin) {
							var escapedCrossOrigin = ("" + crossOrigin).replace(regexForLinkHeaderQuotedParamValueContext, escapeStringForLinkHeaderQuotedParamValueContextReplacer);
							JSCompiler_temp += "; crossorigin=\"" + escapedCrossOrigin + "\"";
						}
						JSCompiler_temp = (header = JSCompiler_temp, 0 <= (resumableState.remainingCapacity -= header.length + 2));
					}
					JSCompiler_temp ? (renderState.resets.connect[bucket][href] = null, resumableState.preconnects && (resumableState.preconnects += ", "), resumableState.preconnects += header) : (bucket = [], pushLinkImpl(bucket, {
						rel: "preconnect",
						href,
						crossOrigin
					}), renderState.preconnects.add(bucket));
				}
				enqueueFlush(request);
			}
		} else previousDispatcher.C(href, crossOrigin);
	}
	function preload(href, as, options) {
		var request = currentRequest ? currentRequest : null;
		if (request) {
			var resumableState = request.resumableState, renderState = request.renderState;
			if (as && href) {
				switch (as) {
					case "image":
						if (options) {
							var imageSrcSet = options.imageSrcSet;
							var imageSizes = options.imageSizes;
							var fetchPriority = options.fetchPriority;
						}
						var key = imageSrcSet ? imageSrcSet + "\n" + (imageSizes || "") : href;
						if (resumableState.imageResources.hasOwnProperty(key)) return;
						resumableState.imageResources[key] = PRELOAD_NO_CREDS;
						resumableState = renderState.headers;
						var header;
						resumableState && 0 < resumableState.remainingCapacity && "string" !== typeof imageSrcSet && "high" === fetchPriority && (header = getPreloadAsHeader(href, as, options), 0 <= (resumableState.remainingCapacity -= header.length + 2)) ? (renderState.resets.image[key] = PRELOAD_NO_CREDS, resumableState.highImagePreloads && (resumableState.highImagePreloads += ", "), resumableState.highImagePreloads += header) : (resumableState = [], pushLinkImpl(resumableState, assign({
							rel: "preload",
							href: imageSrcSet ? void 0 : href,
							as
						}, options)), "high" === fetchPriority ? renderState.highImagePreloads.add(resumableState) : (renderState.bulkPreloads.add(resumableState), renderState.preloads.images.set(key, resumableState)));
						break;
					case "style":
						if (resumableState.styleResources.hasOwnProperty(href)) return;
						imageSrcSet = [];
						pushLinkImpl(imageSrcSet, assign({
							rel: "preload",
							href,
							as
						}, options));
						resumableState.styleResources[href] = !options || "string" !== typeof options.crossOrigin && "string" !== typeof options.integrity ? PRELOAD_NO_CREDS : [options.crossOrigin, options.integrity];
						renderState.preloads.stylesheets.set(href, imageSrcSet);
						renderState.bulkPreloads.add(imageSrcSet);
						break;
					case "script":
						if (resumableState.scriptResources.hasOwnProperty(href)) return;
						imageSrcSet = [];
						renderState.preloads.scripts.set(href, imageSrcSet);
						renderState.bulkPreloads.add(imageSrcSet);
						pushLinkImpl(imageSrcSet, assign({
							rel: "preload",
							href,
							as
						}, options));
						resumableState.scriptResources[href] = !options || "string" !== typeof options.crossOrigin && "string" !== typeof options.integrity ? PRELOAD_NO_CREDS : [options.crossOrigin, options.integrity];
						break;
					default:
						if (resumableState.unknownResources.hasOwnProperty(as)) {
							if (imageSrcSet = resumableState.unknownResources[as], imageSrcSet.hasOwnProperty(href)) return;
						} else imageSrcSet = {}, resumableState.unknownResources[as] = imageSrcSet;
						imageSrcSet[href] = PRELOAD_NO_CREDS;
						if ((resumableState = renderState.headers) && 0 < resumableState.remainingCapacity && "font" === as && (key = getPreloadAsHeader(href, as, options), 0 <= (resumableState.remainingCapacity -= key.length + 2))) renderState.resets.font[href] = PRELOAD_NO_CREDS, resumableState.fontPreloads && (resumableState.fontPreloads += ", "), resumableState.fontPreloads += key;
						else switch (resumableState = [], href = assign({
							rel: "preload",
							href,
							as
						}, options), pushLinkImpl(resumableState, href), as) {
							case "font":
								renderState.fontPreloads.add(resumableState);
								break;
							default: renderState.bulkPreloads.add(resumableState);
						}
				}
				enqueueFlush(request);
			}
		} else previousDispatcher.L(href, as, options);
	}
	function preloadModule(href, options) {
		var request = currentRequest ? currentRequest : null;
		if (request) {
			var resumableState = request.resumableState, renderState = request.renderState;
			if (href) {
				var as = options && "string" === typeof options.as ? options.as : "script";
				switch (as) {
					case "script":
						if (resumableState.moduleScriptResources.hasOwnProperty(href)) return;
						as = [];
						resumableState.moduleScriptResources[href] = !options || "string" !== typeof options.crossOrigin && "string" !== typeof options.integrity ? PRELOAD_NO_CREDS : [options.crossOrigin, options.integrity];
						renderState.preloads.moduleScripts.set(href, as);
						break;
					default:
						if (resumableState.moduleUnknownResources.hasOwnProperty(as)) {
							var resources = resumableState.unknownResources[as];
							if (resources.hasOwnProperty(href)) return;
						} else resources = {}, resumableState.moduleUnknownResources[as] = resources;
						as = [];
						resources[href] = PRELOAD_NO_CREDS;
				}
				pushLinkImpl(as, assign({
					rel: "modulepreload",
					href
				}, options));
				renderState.bulkPreloads.add(as);
				enqueueFlush(request);
			}
		} else previousDispatcher.m(href, options);
	}
	function preinitStyle(href, precedence, options) {
		var request = currentRequest ? currentRequest : null;
		if (request) {
			var resumableState = request.resumableState, renderState = request.renderState;
			if (href) {
				precedence = precedence || "default";
				var styleQueue = renderState.styles.get(precedence), resourceState = resumableState.styleResources.hasOwnProperty(href) ? resumableState.styleResources[href] : void 0;
				null !== resourceState && (resumableState.styleResources[href] = null, styleQueue || (styleQueue = {
					precedence: escapeTextForBrowser(precedence),
					rules: [],
					hrefs: [],
					sheets: /* @__PURE__ */ new Map()
				}, renderState.styles.set(precedence, styleQueue)), precedence = {
					state: 0,
					props: assign({
						rel: "stylesheet",
						href,
						"data-precedence": precedence
					}, options)
				}, resourceState && (2 === resourceState.length && adoptPreloadCredentials(precedence.props, resourceState), (renderState = renderState.preloads.stylesheets.get(href)) && 0 < renderState.length ? renderState.length = 0 : precedence.state = 1), styleQueue.sheets.set(href, precedence), enqueueFlush(request));
			}
		} else previousDispatcher.S(href, precedence, options);
	}
	function preinitScript(src, options) {
		var request = currentRequest ? currentRequest : null;
		if (request) {
			var resumableState = request.resumableState, renderState = request.renderState;
			if (src) {
				var resourceState = resumableState.scriptResources.hasOwnProperty(src) ? resumableState.scriptResources[src] : void 0;
				null !== resourceState && (resumableState.scriptResources[src] = null, options = assign({
					src,
					async: !0
				}, options), resourceState && (2 === resourceState.length && adoptPreloadCredentials(options, resourceState), src = renderState.preloads.scripts.get(src)) && (src.length = 0), src = [], renderState.scripts.add(src), pushScriptImpl(src, options), enqueueFlush(request));
			}
		} else previousDispatcher.X(src, options);
	}
	function preinitModuleScript(src, options) {
		var request = currentRequest ? currentRequest : null;
		if (request) {
			var resumableState = request.resumableState, renderState = request.renderState;
			if (src) {
				var resourceState = resumableState.moduleScriptResources.hasOwnProperty(src) ? resumableState.moduleScriptResources[src] : void 0;
				null !== resourceState && (resumableState.moduleScriptResources[src] = null, options = assign({
					src,
					type: "module",
					async: !0
				}, options), resourceState && (2 === resourceState.length && adoptPreloadCredentials(options, resourceState), src = renderState.preloads.moduleScripts.get(src)) && (src.length = 0), src = [], renderState.scripts.add(src), pushScriptImpl(src, options), enqueueFlush(request));
			}
		} else previousDispatcher.M(src, options);
	}
	function adoptPreloadCredentials(target, preloadState) {
		target.crossOrigin ??= preloadState[0];
		target.integrity ??= preloadState[1];
	}
	function getPreloadAsHeader(href, as, params) {
		href = ("" + href).replace(regexForHrefInLinkHeaderURLContext, escapeHrefForLinkHeaderURLContextReplacer);
		as = ("" + as).replace(regexForLinkHeaderQuotedParamValueContext, escapeStringForLinkHeaderQuotedParamValueContextReplacer);
		as = "<" + href + ">; rel=preload; as=\"" + as + "\"";
		for (var paramName in params) hasOwnProperty.call(params, paramName) && (href = params[paramName], "string" === typeof href && (as += "; " + paramName.toLowerCase() + "=\"" + ("" + href).replace(regexForLinkHeaderQuotedParamValueContext, escapeStringForLinkHeaderQuotedParamValueContextReplacer) + "\""));
		return as;
	}
	var regexForHrefInLinkHeaderURLContext = /[<>\r\n]/g;
	function escapeHrefForLinkHeaderURLContextReplacer(match) {
		switch (match) {
			case "<": return "%3C";
			case ">": return "%3E";
			case "\n": return "%0A";
			case "\r": return "%0D";
			default: throw Error("escapeLinkHrefForHeaderContextReplacer encountered a match it does not know how to replace. this means the match regex and the replacement characters are no longer in sync. This is a bug in React");
		}
	}
	var regexForLinkHeaderQuotedParamValueContext = /["';,\r\n]/g;
	function escapeStringForLinkHeaderQuotedParamValueContextReplacer(match) {
		switch (match) {
			case "\"": return "%22";
			case "'": return "%27";
			case ";": return "%3B";
			case ",": return "%2C";
			case "\n": return "%0A";
			case "\r": return "%0D";
			default: throw Error("escapeStringForLinkHeaderQuotedParamValueContextReplacer encountered a match it does not know how to replace. this means the match regex and the replacement characters are no longer in sync. This is a bug in React");
		}
	}
	function hoistStyleQueueDependency(styleQueue) {
		this.styles.add(styleQueue);
	}
	function hoistStylesheetDependency(stylesheet) {
		this.stylesheets.add(stylesheet);
	}
	function hoistHoistables(parentState, childState) {
		childState.styles.forEach(hoistStyleQueueDependency, parentState);
		childState.stylesheets.forEach(hoistStylesheetDependency, parentState);
		childState.suspenseyImages && (parentState.suspenseyImages = !0);
	}
	function createRenderState(resumableState, generateStaticMarkup) {
		var idPrefix = resumableState.idPrefix, bootstrapChunks = [], bootstrapScriptContent = resumableState.bootstrapScriptContent, bootstrapScripts = resumableState.bootstrapScripts, bootstrapModules = resumableState.bootstrapModules;
		void 0 !== bootstrapScriptContent && (bootstrapChunks.push("<script"), pushCompletedShellIdAttribute(bootstrapChunks, resumableState), bootstrapChunks.push(">", ("" + bootstrapScriptContent).replace(scriptRegex, scriptReplacer), "<\/script>"));
		bootstrapScriptContent = idPrefix + "P:";
		var JSCompiler_object_inline_segmentPrefix_1673 = idPrefix + "S:";
		idPrefix += "B:";
		var JSCompiler_object_inline_preconnects_1687 = /* @__PURE__ */ new Set(), JSCompiler_object_inline_fontPreloads_1688 = /* @__PURE__ */ new Set(), JSCompiler_object_inline_highImagePreloads_1689 = /* @__PURE__ */ new Set(), JSCompiler_object_inline_styles_1690 = /* @__PURE__ */ new Map(), JSCompiler_object_inline_bootstrapScripts_1691 = /* @__PURE__ */ new Set(), JSCompiler_object_inline_scripts_1692 = /* @__PURE__ */ new Set(), JSCompiler_object_inline_bulkPreloads_1693 = /* @__PURE__ */ new Set(), JSCompiler_object_inline_preloads_1694 = {
			images: /* @__PURE__ */ new Map(),
			stylesheets: /* @__PURE__ */ new Map(),
			scripts: /* @__PURE__ */ new Map(),
			moduleScripts: /* @__PURE__ */ new Map()
		};
		if (void 0 !== bootstrapScripts) for (var i = 0; i < bootstrapScripts.length; i++) {
			var scriptConfig = bootstrapScripts[i], src, crossOrigin = void 0, integrity = void 0, props = {
				rel: "preload",
				as: "script",
				fetchPriority: "low",
				nonce: void 0
			};
			"string" === typeof scriptConfig ? props.href = src = scriptConfig : (props.href = src = scriptConfig.src, props.integrity = integrity = "string" === typeof scriptConfig.integrity ? scriptConfig.integrity : void 0, props.crossOrigin = crossOrigin = "string" === typeof scriptConfig || null == scriptConfig.crossOrigin ? void 0 : "use-credentials" === scriptConfig.crossOrigin ? "use-credentials" : "");
			scriptConfig = resumableState;
			var href = src;
			scriptConfig.scriptResources[href] = null;
			scriptConfig.moduleScriptResources[href] = null;
			scriptConfig = [];
			pushLinkImpl(scriptConfig, props);
			JSCompiler_object_inline_bootstrapScripts_1691.add(scriptConfig);
			bootstrapChunks.push("<script src=\"", escapeTextForBrowser(src), "\"");
			"string" === typeof integrity && bootstrapChunks.push(" integrity=\"", escapeTextForBrowser(integrity), "\"");
			"string" === typeof crossOrigin && bootstrapChunks.push(" crossorigin=\"", escapeTextForBrowser(crossOrigin), "\"");
			pushCompletedShellIdAttribute(bootstrapChunks, resumableState);
			bootstrapChunks.push(" async=\"\"><\/script>");
		}
		if (void 0 !== bootstrapModules) for (bootstrapScripts = 0; bootstrapScripts < bootstrapModules.length; bootstrapScripts++) props = bootstrapModules[bootstrapScripts], crossOrigin = src = void 0, integrity = {
			rel: "modulepreload",
			fetchPriority: "low",
			nonce: void 0
		}, "string" === typeof props ? integrity.href = i = props : (integrity.href = i = props.src, integrity.integrity = crossOrigin = "string" === typeof props.integrity ? props.integrity : void 0, integrity.crossOrigin = src = "string" === typeof props || null == props.crossOrigin ? void 0 : "use-credentials" === props.crossOrigin ? "use-credentials" : ""), props = resumableState, scriptConfig = i, props.scriptResources[scriptConfig] = null, props.moduleScriptResources[scriptConfig] = null, props = [], pushLinkImpl(props, integrity), JSCompiler_object_inline_bootstrapScripts_1691.add(props), bootstrapChunks.push("<script type=\"module\" src=\"", escapeTextForBrowser(i), "\""), "string" === typeof crossOrigin && bootstrapChunks.push(" integrity=\"", escapeTextForBrowser(crossOrigin), "\""), "string" === typeof src && bootstrapChunks.push(" crossorigin=\"", escapeTextForBrowser(src), "\""), pushCompletedShellIdAttribute(bootstrapChunks, resumableState), bootstrapChunks.push(" async=\"\"><\/script>");
		return {
			placeholderPrefix: bootstrapScriptContent,
			segmentPrefix: JSCompiler_object_inline_segmentPrefix_1673,
			boundaryPrefix: idPrefix,
			startInlineScript: "<script",
			startInlineStyle: "<style",
			preamble: {
				htmlChunks: null,
				headChunks: null,
				bodyChunks: null
			},
			externalRuntimeScript: null,
			bootstrapChunks,
			importMapChunks: [],
			onHeaders: void 0,
			headers: null,
			resets: {
				font: {},
				dns: {},
				connect: {
					default: {},
					anonymous: {},
					credentials: {}
				},
				image: {},
				style: {}
			},
			charsetChunks: [],
			viewportChunks: [],
			hoistableChunks: [],
			preconnects: JSCompiler_object_inline_preconnects_1687,
			fontPreloads: JSCompiler_object_inline_fontPreloads_1688,
			highImagePreloads: JSCompiler_object_inline_highImagePreloads_1689,
			styles: JSCompiler_object_inline_styles_1690,
			bootstrapScripts: JSCompiler_object_inline_bootstrapScripts_1691,
			scripts: JSCompiler_object_inline_scripts_1692,
			bulkPreloads: JSCompiler_object_inline_bulkPreloads_1693,
			preloads: JSCompiler_object_inline_preloads_1694,
			nonce: {
				script: void 0,
				style: void 0
			},
			stylesToHoist: !1,
			generateStaticMarkup
		};
	}
	function pushTextInstance(target, text, renderState, textEmbedded) {
		if (renderState.generateStaticMarkup) return target.push(escapeTextForBrowser(text)), !1;
		"" === text ? target = textEmbedded : (textEmbedded && target.push("<!-- -->"), target.push(escapeTextForBrowser(text)), target = !0);
		return target;
	}
	function pushSegmentFinale(target, renderState, lastPushedText, textEmbedded) {
		renderState.generateStaticMarkup || lastPushedText && textEmbedded && target.push("<!-- -->");
	}
	var bind = Function.prototype.bind;
	var REACT_CLIENT_REFERENCE = Symbol.for("react.client.reference");
	function getComponentNameFromType(type) {
		if (null == type) return null;
		if ("function" === typeof type) return type.$$typeof === REACT_CLIENT_REFERENCE ? null : type.displayName || type.name || null;
		if ("string" === typeof type) return type;
		switch (type) {
			case REACT_FRAGMENT_TYPE: return "Fragment";
			case REACT_PROFILER_TYPE: return "Profiler";
			case REACT_STRICT_MODE_TYPE: return "StrictMode";
			case REACT_SUSPENSE_TYPE: return "Suspense";
			case REACT_SUSPENSE_LIST_TYPE: return "SuspenseList";
			case REACT_ACTIVITY_TYPE: return "Activity";
		}
		if ("object" === typeof type) switch (type.$$typeof) {
			case REACT_PORTAL_TYPE: return "Portal";
			case REACT_CONTEXT_TYPE: return type.displayName || "Context";
			case REACT_CONSUMER_TYPE: return (type._context.displayName || "Context") + ".Consumer";
			case REACT_FORWARD_REF_TYPE:
				var innerType = type.render;
				type = type.displayName;
				type || (type = innerType.displayName || innerType.name || "", type = "" !== type ? "ForwardRef(" + type + ")" : "ForwardRef");
				return type;
			case REACT_MEMO_TYPE: return innerType = type.displayName || null, null !== innerType ? innerType : getComponentNameFromType(type.type) || "Memo";
			case REACT_LAZY_TYPE:
				innerType = type._payload;
				type = type._init;
				try {
					return getComponentNameFromType(type(innerType));
				} catch (x) {}
		}
		return null;
	}
	var emptyContextObject = {};
	var currentActiveSnapshot = null;
	function popToNearestCommonAncestor(prev, next) {
		if (prev !== next) {
			prev.context._currentValue2 = prev.parentValue;
			prev = prev.parent;
			var parentNext = next.parent;
			if (null === prev) {
				if (null !== parentNext) throw Error(formatProdErrorMessage(401));
			} else {
				if (null === parentNext) throw Error(formatProdErrorMessage(401));
				popToNearestCommonAncestor(prev, parentNext);
			}
			next.context._currentValue2 = next.value;
		}
	}
	function popAllPrevious(prev) {
		prev.context._currentValue2 = prev.parentValue;
		prev = prev.parent;
		null !== prev && popAllPrevious(prev);
	}
	function pushAllNext(next) {
		var parentNext = next.parent;
		null !== parentNext && pushAllNext(parentNext);
		next.context._currentValue2 = next.value;
	}
	function popPreviousToCommonLevel(prev, next) {
		prev.context._currentValue2 = prev.parentValue;
		prev = prev.parent;
		if (null === prev) throw Error(formatProdErrorMessage(402));
		prev.depth === next.depth ? popToNearestCommonAncestor(prev, next) : popPreviousToCommonLevel(prev, next);
	}
	function popNextToCommonLevel(prev, next) {
		var parentNext = next.parent;
		if (null === parentNext) throw Error(formatProdErrorMessage(402));
		prev.depth === parentNext.depth ? popToNearestCommonAncestor(prev, parentNext) : popNextToCommonLevel(prev, parentNext);
		next.context._currentValue2 = next.value;
	}
	function switchContext(newSnapshot) {
		var prev = currentActiveSnapshot;
		prev !== newSnapshot && (null === prev ? pushAllNext(newSnapshot) : null === newSnapshot ? popAllPrevious(prev) : prev.depth === newSnapshot.depth ? popToNearestCommonAncestor(prev, newSnapshot) : prev.depth > newSnapshot.depth ? popPreviousToCommonLevel(prev, newSnapshot) : popNextToCommonLevel(prev, newSnapshot), currentActiveSnapshot = newSnapshot);
	}
	var classComponentUpdater = {
		enqueueSetState: function(inst, payload) {
			inst = inst._reactInternals;
			null !== inst.queue && inst.queue.push(payload);
		},
		enqueueReplaceState: function(inst, payload) {
			inst = inst._reactInternals;
			inst.replace = !0;
			inst.queue = [payload];
		},
		enqueueForceUpdate: function() {}
	};
	var emptyTreeContext = {
		id: 1,
		overflow: ""
	};
	function pushTreeContext(baseContext, totalChildren, index) {
		var baseIdWithLeadingBit = baseContext.id;
		baseContext = baseContext.overflow;
		var baseLength = 32 - clz32(baseIdWithLeadingBit) - 1;
		baseIdWithLeadingBit &= ~(1 << baseLength);
		index += 1;
		var length = 32 - clz32(totalChildren) + baseLength;
		if (30 < length) {
			var numberOfOverflowBits = baseLength - baseLength % 5;
			length = (baseIdWithLeadingBit & (1 << numberOfOverflowBits) - 1).toString(32);
			baseIdWithLeadingBit >>= numberOfOverflowBits;
			baseLength -= numberOfOverflowBits;
			return {
				id: 1 << 32 - clz32(totalChildren) + baseLength | index << baseLength | baseIdWithLeadingBit,
				overflow: length + baseContext
			};
		}
		return {
			id: 1 << length | index << baseLength | baseIdWithLeadingBit,
			overflow: baseContext
		};
	}
	var clz32 = Math.clz32 ? Math.clz32 : clz32Fallback;
	var log = Math.log;
	var LN2 = Math.LN2;
	function clz32Fallback(x) {
		x >>>= 0;
		return 0 === x ? 32 : 31 - (log(x) / LN2 | 0) | 0;
	}
	function noop() {}
	var SuspenseException = Error(formatProdErrorMessage(460));
	function trackUsedThenable(thenableState, thenable, index) {
		index = thenableState[index];
		void 0 === index ? thenableState.push(thenable) : index !== thenable && (thenable.then(noop, noop), thenable = index);
		switch (thenable.status) {
			case "fulfilled": return thenable.value;
			case "rejected": throw thenable.reason;
			default:
				"string" === typeof thenable.status ? thenable.then(noop, noop) : (thenableState = thenable, thenableState.status = "pending", thenableState.then(function(fulfilledValue) {
					if ("pending" === thenable.status) {
						var fulfilledThenable = thenable;
						fulfilledThenable.status = "fulfilled";
						fulfilledThenable.value = fulfilledValue;
					}
				}, function(error) {
					if ("pending" === thenable.status) {
						var rejectedThenable = thenable;
						rejectedThenable.status = "rejected";
						rejectedThenable.reason = error;
					}
				}));
				switch (thenable.status) {
					case "fulfilled": return thenable.value;
					case "rejected": throw thenable.reason;
				}
				suspendedThenable = thenable;
				throw SuspenseException;
		}
	}
	var suspendedThenable = null;
	function getSuspendedThenable() {
		if (null === suspendedThenable) throw Error(formatProdErrorMessage(459));
		var thenable = suspendedThenable;
		suspendedThenable = null;
		return thenable;
	}
	function is(x, y) {
		return x === y && (0 !== x || 1 / x === 1 / y) || x !== x && y !== y;
	}
	var objectIs = "function" === typeof Object.is ? Object.is : is;
	var currentlyRenderingComponent = null;
	var currentlyRenderingTask = null;
	var currentlyRenderingRequest = null;
	var currentlyRenderingKeyPath = null;
	var firstWorkInProgressHook = null;
	var workInProgressHook = null;
	var isReRender = !1;
	var didScheduleRenderPhaseUpdate = !1;
	var localIdCounter = 0;
	var actionStateCounter = 0;
	var actionStateMatchingIndex = -1;
	var thenableIndexCounter = 0;
	var thenableState = null;
	var renderPhaseUpdates = null;
	var numberOfReRenders = 0;
	function resolveCurrentlyRenderingComponent() {
		if (null === currentlyRenderingComponent) throw Error(formatProdErrorMessage(321));
		return currentlyRenderingComponent;
	}
	function createHook() {
		if (0 < numberOfReRenders) throw Error(formatProdErrorMessage(312));
		return {
			memoizedState: null,
			queue: null,
			next: null
		};
	}
	function createWorkInProgressHook() {
		null === workInProgressHook ? null === firstWorkInProgressHook ? (isReRender = !1, firstWorkInProgressHook = workInProgressHook = createHook()) : (isReRender = !0, workInProgressHook = firstWorkInProgressHook) : null === workInProgressHook.next ? (isReRender = !1, workInProgressHook = workInProgressHook.next = createHook()) : (isReRender = !0, workInProgressHook = workInProgressHook.next);
		return workInProgressHook;
	}
	function getThenableStateAfterSuspending() {
		var state = thenableState;
		thenableState = null;
		return state;
	}
	function resetHooksState() {
		currentlyRenderingKeyPath = currentlyRenderingRequest = currentlyRenderingTask = currentlyRenderingComponent = null;
		didScheduleRenderPhaseUpdate = !1;
		firstWorkInProgressHook = null;
		numberOfReRenders = 0;
		workInProgressHook = renderPhaseUpdates = null;
	}
	function basicStateReducer(state, action) {
		return "function" === typeof action ? action(state) : action;
	}
	function useReducer(reducer, initialArg, init) {
		currentlyRenderingComponent = resolveCurrentlyRenderingComponent();
		workInProgressHook = createWorkInProgressHook();
		if (isReRender) {
			var queue = workInProgressHook.queue;
			initialArg = queue.dispatch;
			if (null !== renderPhaseUpdates && (init = renderPhaseUpdates.get(queue), void 0 !== init)) {
				renderPhaseUpdates.delete(queue);
				queue = workInProgressHook.memoizedState;
				do
					queue = reducer(queue, init.action), init = init.next;
				while (null !== init);
				workInProgressHook.memoizedState = queue;
				return [queue, initialArg];
			}
			return [workInProgressHook.memoizedState, initialArg];
		}
		reducer = reducer === basicStateReducer ? "function" === typeof initialArg ? initialArg() : initialArg : void 0 !== init ? init(initialArg) : initialArg;
		workInProgressHook.memoizedState = reducer;
		reducer = workInProgressHook.queue = {
			last: null,
			dispatch: null
		};
		reducer = reducer.dispatch = dispatchAction.bind(null, currentlyRenderingComponent, reducer);
		return [workInProgressHook.memoizedState, reducer];
	}
	function useMemo(nextCreate, deps) {
		currentlyRenderingComponent = resolveCurrentlyRenderingComponent();
		workInProgressHook = createWorkInProgressHook();
		deps = void 0 === deps ? null : deps;
		if (null !== workInProgressHook) {
			var prevState = workInProgressHook.memoizedState;
			if (null !== prevState && null !== deps) {
				var prevDeps = prevState[1];
				a: if (null === prevDeps) prevDeps = !1;
				else {
					for (var i = 0; i < prevDeps.length && i < deps.length; i++) if (!objectIs(deps[i], prevDeps[i])) {
						prevDeps = !1;
						break a;
					}
					prevDeps = !0;
				}
				if (prevDeps) return prevState[0];
			}
		}
		nextCreate = nextCreate();
		workInProgressHook.memoizedState = [nextCreate, deps];
		return nextCreate;
	}
	function dispatchAction(componentIdentity, queue, action) {
		if (25 <= numberOfReRenders) throw Error(formatProdErrorMessage(301));
		if (componentIdentity === currentlyRenderingComponent) if (didScheduleRenderPhaseUpdate = !0, componentIdentity = {
			action,
			next: null
		}, null === renderPhaseUpdates && (renderPhaseUpdates = /* @__PURE__ */ new Map()), action = renderPhaseUpdates.get(queue), void 0 === action) renderPhaseUpdates.set(queue, componentIdentity);
		else {
			for (queue = action; null !== queue.next;) queue = queue.next;
			queue.next = componentIdentity;
		}
	}
	function throwOnUseEffectEventCall() {
		throw Error(formatProdErrorMessage(440));
	}
	function unsupportedStartTransition() {
		throw Error(formatProdErrorMessage(394));
	}
	function unsupportedSetOptimisticState() {
		throw Error(formatProdErrorMessage(479));
	}
	function useActionState(action, initialState, permalink) {
		resolveCurrentlyRenderingComponent();
		var actionStateHookIndex = actionStateCounter++, request = currentlyRenderingRequest;
		if ("function" === typeof action.$$FORM_ACTION) {
			var nextPostbackStateKey = null, componentKeyPath = currentlyRenderingKeyPath;
			request = request.formState;
			var isSignatureEqual = action.$$IS_SIGNATURE_EQUAL;
			if (null !== request && "function" === typeof isSignatureEqual) {
				var postbackKey = request[1];
				isSignatureEqual.call(action, request[2], request[3]) && (nextPostbackStateKey = void 0 !== permalink ? "p" + permalink : "k" + murmurhash3_32_gc(JSON.stringify([
					componentKeyPath,
					null,
					actionStateHookIndex
				]), 0), postbackKey === nextPostbackStateKey && (actionStateMatchingIndex = actionStateHookIndex, initialState = request[0]));
			}
			var boundAction = action.bind(null, initialState);
			action = function(payload) {
				boundAction(payload);
			};
			"function" === typeof boundAction.$$FORM_ACTION && (action.$$FORM_ACTION = function(prefix) {
				prefix = boundAction.$$FORM_ACTION(prefix);
				void 0 !== permalink && (permalink += "", prefix.action = permalink);
				var formData = prefix.data;
				formData && (null === nextPostbackStateKey && (nextPostbackStateKey = void 0 !== permalink ? "p" + permalink : "k" + murmurhash3_32_gc(JSON.stringify([
					componentKeyPath,
					null,
					actionStateHookIndex
				]), 0)), formData.append("$ACTION_KEY", nextPostbackStateKey));
				return prefix;
			});
			return [
				initialState,
				action,
				!1
			];
		}
		var boundAction$22 = action.bind(null, initialState);
		return [
			initialState,
			function(payload) {
				boundAction$22(payload);
			},
			!1
		];
	}
	function unwrapThenable(thenable) {
		var index = thenableIndexCounter;
		thenableIndexCounter += 1;
		null === thenableState && (thenableState = []);
		return trackUsedThenable(thenableState, thenable, index);
	}
	function unsupportedRefresh() {
		throw Error(formatProdErrorMessage(393));
	}
	var HooksDispatcher = {
		readContext: function(context) {
			return context._currentValue2;
		},
		use: function(usable) {
			if (null !== usable && "object" === typeof usable) {
				if ("function" === typeof usable.then) return unwrapThenable(usable);
				if (usable.$$typeof === REACT_CONTEXT_TYPE) return usable._currentValue2;
			}
			throw Error(formatProdErrorMessage(438, String(usable)));
		},
		useContext: function(context) {
			resolveCurrentlyRenderingComponent();
			return context._currentValue2;
		},
		useMemo,
		useReducer,
		useRef: function(initialValue) {
			currentlyRenderingComponent = resolveCurrentlyRenderingComponent();
			workInProgressHook = createWorkInProgressHook();
			var previousRef = workInProgressHook.memoizedState;
			return null === previousRef ? (initialValue = { current: initialValue }, workInProgressHook.memoizedState = initialValue) : previousRef;
		},
		useState: function(initialState) {
			return useReducer(basicStateReducer, initialState);
		},
		useInsertionEffect: noop,
		useLayoutEffect: noop,
		useCallback: function(callback, deps) {
			return useMemo(function() {
				return callback;
			}, deps);
		},
		useImperativeHandle: noop,
		useEffect: noop,
		useDebugValue: noop,
		useDeferredValue: function(value, initialValue) {
			resolveCurrentlyRenderingComponent();
			return void 0 !== initialValue ? initialValue : value;
		},
		useTransition: function() {
			resolveCurrentlyRenderingComponent();
			return [!1, unsupportedStartTransition];
		},
		useId: function() {
			var JSCompiler_inline_result = currentlyRenderingTask.treeContext;
			var overflow = JSCompiler_inline_result.overflow;
			JSCompiler_inline_result = JSCompiler_inline_result.id;
			JSCompiler_inline_result = (JSCompiler_inline_result & ~(1 << 32 - clz32(JSCompiler_inline_result) - 1)).toString(32) + overflow;
			var resumableState = currentResumableState;
			if (null === resumableState) throw Error(formatProdErrorMessage(404));
			overflow = localIdCounter++;
			JSCompiler_inline_result = "_" + resumableState.idPrefix + "R_" + JSCompiler_inline_result;
			0 < overflow && (JSCompiler_inline_result += "H" + overflow.toString(32));
			return JSCompiler_inline_result + "_";
		},
		useSyncExternalStore: function(subscribe, getSnapshot, getServerSnapshot) {
			if (void 0 === getServerSnapshot) throw Error(formatProdErrorMessage(407));
			return getServerSnapshot();
		},
		useOptimistic: function(passthrough) {
			resolveCurrentlyRenderingComponent();
			return [passthrough, unsupportedSetOptimisticState];
		},
		useActionState,
		useFormState: useActionState,
		useHostTransitionStatus: function() {
			resolveCurrentlyRenderingComponent();
			return sharedNotPendingObject;
		},
		useMemoCache: function(size) {
			for (var data = Array(size), i = 0; i < size; i++) data[i] = REACT_MEMO_CACHE_SENTINEL;
			return data;
		},
		useCacheRefresh: function() {
			return unsupportedRefresh;
		},
		useEffectEvent: function() {
			return throwOnUseEffectEventCall;
		}
	};
	var currentResumableState = null;
	var DefaultAsyncDispatcher = {
		getCacheForType: function() {
			throw Error(formatProdErrorMessage(248));
		},
		cacheSignal: function() {
			throw Error(formatProdErrorMessage(248));
		}
	};
	var prefix;
	var suffix;
	function describeBuiltInComponentFrame(name) {
		if (void 0 === prefix) try {
			throw Error();
		} catch (x) {
			var match = x.stack.trim().match(/\n( *(at )?)/);
			prefix = match && match[1] || "";
			suffix = -1 < x.stack.indexOf("\n    at") ? " (<anonymous>)" : -1 < x.stack.indexOf("@") ? "@unknown:0:0" : "";
		}
		return "\n" + prefix + name + suffix;
	}
	var reentry = !1;
	function describeNativeComponentFrame(fn, construct) {
		if (!fn || reentry) return "";
		reentry = !0;
		var previousPrepareStackTrace = Error.prepareStackTrace;
		Error.prepareStackTrace = void 0;
		try {
			var RunInRootFrame = { DetermineComponentFrameRoot: function() {
				try {
					if (construct) {
						var Fake = function() {
							throw Error();
						};
						Object.defineProperty(Fake.prototype, "props", { set: function() {
							throw Error();
						} });
						if ("object" === typeof Reflect && Reflect.construct) {
							try {
								Reflect.construct(Fake, []);
							} catch (x) {
								var control = x;
							}
							Reflect.construct(fn, [], Fake);
						} else {
							try {
								Fake.call();
							} catch (x$24) {
								control = x$24;
							}
							fn.call(Fake.prototype);
						}
					} else {
						try {
							throw Error();
						} catch (x$25) {
							control = x$25;
						}
						(Fake = fn()) && "function" === typeof Fake.catch && Fake.catch(function() {});
					}
				} catch (sample) {
					if (sample && control && "string" === typeof sample.stack) return [sample.stack, control.stack];
				}
				return [null, null];
			} };
			RunInRootFrame.DetermineComponentFrameRoot.displayName = "DetermineComponentFrameRoot";
			var namePropDescriptor = Object.getOwnPropertyDescriptor(RunInRootFrame.DetermineComponentFrameRoot, "name");
			namePropDescriptor && namePropDescriptor.configurable && Object.defineProperty(RunInRootFrame.DetermineComponentFrameRoot, "name", { value: "DetermineComponentFrameRoot" });
			var _RunInRootFrame$Deter = RunInRootFrame.DetermineComponentFrameRoot(), sampleStack = _RunInRootFrame$Deter[0], controlStack = _RunInRootFrame$Deter[1];
			if (sampleStack && controlStack) {
				var sampleLines = sampleStack.split("\n"), controlLines = controlStack.split("\n");
				for (namePropDescriptor = RunInRootFrame = 0; RunInRootFrame < sampleLines.length && !sampleLines[RunInRootFrame].includes("DetermineComponentFrameRoot");) RunInRootFrame++;
				for (; namePropDescriptor < controlLines.length && !controlLines[namePropDescriptor].includes("DetermineComponentFrameRoot");) namePropDescriptor++;
				if (RunInRootFrame === sampleLines.length || namePropDescriptor === controlLines.length) for (RunInRootFrame = sampleLines.length - 1, namePropDescriptor = controlLines.length - 1; 1 <= RunInRootFrame && 0 <= namePropDescriptor && sampleLines[RunInRootFrame] !== controlLines[namePropDescriptor];) namePropDescriptor--;
				for (; 1 <= RunInRootFrame && 0 <= namePropDescriptor; RunInRootFrame--, namePropDescriptor--) if (sampleLines[RunInRootFrame] !== controlLines[namePropDescriptor]) {
					if (1 !== RunInRootFrame || 1 !== namePropDescriptor) do
						if (RunInRootFrame--, namePropDescriptor--, 0 > namePropDescriptor || sampleLines[RunInRootFrame] !== controlLines[namePropDescriptor]) {
							var frame = "\n" + sampleLines[RunInRootFrame].replace(" at new ", " at ");
							fn.displayName && frame.includes("<anonymous>") && (frame = frame.replace("<anonymous>", fn.displayName));
							return frame;
						}
					while (1 <= RunInRootFrame && 0 <= namePropDescriptor);
					break;
				}
			}
		} finally {
			reentry = !1, Error.prepareStackTrace = previousPrepareStackTrace;
		}
		return (previousPrepareStackTrace = fn ? fn.displayName || fn.name : "") ? describeBuiltInComponentFrame(previousPrepareStackTrace) : "";
	}
	function describeComponentStackByType(type) {
		if ("string" === typeof type) return describeBuiltInComponentFrame(type);
		if ("function" === typeof type) return type.prototype && type.prototype.isReactComponent ? describeNativeComponentFrame(type, !0) : describeNativeComponentFrame(type, !1);
		if ("object" === typeof type && null !== type) {
			switch (type.$$typeof) {
				case REACT_FORWARD_REF_TYPE: return describeNativeComponentFrame(type.render, !1);
				case REACT_MEMO_TYPE: return describeNativeComponentFrame(type.type, !1);
				case REACT_LAZY_TYPE:
					var lazyComponent = type, payload = lazyComponent._payload;
					lazyComponent = lazyComponent._init;
					try {
						type = lazyComponent(payload);
					} catch (x) {
						return describeBuiltInComponentFrame("Lazy");
					}
					return describeComponentStackByType(type);
			}
			if ("string" === typeof type.name) {
				a: {
					payload = type.name;
					lazyComponent = type.env;
					var location = type.debugLocation;
					if (null != location && (type = Error.prepareStackTrace, Error.prepareStackTrace = void 0, location = location.stack, Error.prepareStackTrace = type, location.startsWith("Error: react-stack-top-frame\n") && (location = location.slice(29)), type = location.indexOf("\n"), -1 !== type && (location = location.slice(type + 1)), type = location.indexOf("react_stack_bottom_frame"), -1 !== type && (type = location.lastIndexOf("\n", type)), type = -1 !== type ? location = location.slice(0, type) : "", location = type.lastIndexOf("\n"), type = -1 === location ? type : type.slice(location + 1), -1 !== type.indexOf(payload))) {
						payload = "\n" + type;
						break a;
					}
					payload = describeBuiltInComponentFrame(payload + (lazyComponent ? " [" + lazyComponent + "]" : ""));
				}
				return payload;
			}
		}
		switch (type) {
			case REACT_SUSPENSE_LIST_TYPE: return describeBuiltInComponentFrame("SuspenseList");
			case REACT_SUSPENSE_TYPE: return describeBuiltInComponentFrame("Suspense");
		}
		return "";
	}
	function isEligibleForOutlining(request, boundary) {
		return (500 < boundary.byteSize || !1) && null === boundary.contentPreamble;
	}
	function defaultErrorHandler(error) {
		if ("object" === typeof error && null !== error && "string" === typeof error.environmentName) {
			var JSCompiler_inline_result = error.environmentName;
			error = [error].slice(0);
			"string" === typeof error[0] ? error.splice(0, 1, "[%s] " + error[0], " " + JSCompiler_inline_result + " ") : error.splice(0, 0, "[%s]", " " + JSCompiler_inline_result + " ");
			error.unshift(console);
			JSCompiler_inline_result = bind.apply(console.error, error);
			JSCompiler_inline_result();
		} else console.error(error);
		return null;
	}
	function RequestInstance(resumableState, renderState, rootFormatContext, progressiveChunkSize, onError, onAllReady, onShellReady, onShellError, onFatalError, onPostpone, formState) {
		var abortSet = /* @__PURE__ */ new Set();
		this.destination = null;
		this.flushScheduled = !1;
		this.resumableState = resumableState;
		this.renderState = renderState;
		this.rootFormatContext = rootFormatContext;
		this.progressiveChunkSize = void 0 === progressiveChunkSize ? 12800 : progressiveChunkSize;
		this.status = 10;
		this.fatalError = null;
		this.pendingRootTasks = this.allPendingTasks = this.nextSegmentId = 0;
		this.completedPreambleSegments = this.completedRootSegment = null;
		this.byteSize = 0;
		this.abortableTasks = abortSet;
		this.pingedTasks = [];
		this.clientRenderedBoundaries = [];
		this.completedBoundaries = [];
		this.partialBoundaries = [];
		this.trackedPostpones = null;
		this.onError = void 0 === onError ? defaultErrorHandler : onError;
		this.onPostpone = void 0 === onPostpone ? noop : onPostpone;
		this.onAllReady = void 0 === onAllReady ? noop : onAllReady;
		this.onShellReady = void 0 === onShellReady ? noop : onShellReady;
		this.onShellError = void 0 === onShellError ? noop : onShellError;
		this.onFatalError = void 0 === onFatalError ? noop : onFatalError;
		this.formState = void 0 === formState ? null : formState;
	}
	function createRequest(children, resumableState, renderState, rootFormatContext, progressiveChunkSize, onError, onAllReady, onShellReady, onShellError, onFatalError, onPostpone, formState) {
		resumableState = new RequestInstance(resumableState, renderState, rootFormatContext, progressiveChunkSize, onError, onAllReady, onShellReady, onShellError, onFatalError, onPostpone, formState);
		renderState = createPendingSegment(resumableState, 0, null, rootFormatContext, !1, !1);
		renderState.parentFlushed = !0;
		children = createRenderTask(resumableState, null, children, -1, null, renderState, null, null, resumableState.abortableTasks, null, rootFormatContext, null, emptyTreeContext, null, null);
		pushComponentStack(children);
		resumableState.pingedTasks.push(children);
		return resumableState;
	}
	var currentRequest = null;
	function pingTask(request, task) {
		request.pingedTasks.push(task);
		1 === request.pingedTasks.length && (request.flushScheduled = null !== request.destination, performWork(request));
	}
	function createSuspenseBoundary(request, row, fallbackAbortableTasks, contentPreamble, fallbackPreamble) {
		fallbackAbortableTasks = {
			status: 0,
			rootSegmentID: -1,
			parentFlushed: !1,
			pendingTasks: 0,
			row,
			completedSegments: [],
			byteSize: 0,
			fallbackAbortableTasks,
			errorDigest: null,
			contentState: createHoistableState(),
			fallbackState: createHoistableState(),
			contentPreamble,
			fallbackPreamble,
			trackedContentKeyPath: null,
			trackedFallbackNode: null
		};
		null !== row && (row.pendingTasks++, contentPreamble = row.boundaries, null !== contentPreamble && (request.allPendingTasks++, fallbackAbortableTasks.pendingTasks++, contentPreamble.push(fallbackAbortableTasks)), request = row.inheritedHoistables, null !== request && hoistHoistables(fallbackAbortableTasks.contentState, request));
		return fallbackAbortableTasks;
	}
	function createRenderTask(request, thenableState, node, childIndex, blockedBoundary, blockedSegment, blockedPreamble, hoistableState, abortSet, keyPath, formatContext, context, treeContext, row, componentStack) {
		request.allPendingTasks++;
		null === blockedBoundary ? request.pendingRootTasks++ : blockedBoundary.pendingTasks++;
		null !== row && row.pendingTasks++;
		var task = {
			replay: null,
			node,
			childIndex,
			ping: function() {
				return pingTask(request, task);
			},
			blockedBoundary,
			blockedSegment,
			blockedPreamble,
			hoistableState,
			abortSet,
			keyPath,
			formatContext,
			context,
			treeContext,
			row,
			componentStack,
			thenableState
		};
		abortSet.add(task);
		return task;
	}
	function createReplayTask(request, thenableState, replay, node, childIndex, blockedBoundary, hoistableState, abortSet, keyPath, formatContext, context, treeContext, row, componentStack) {
		request.allPendingTasks++;
		null === blockedBoundary ? request.pendingRootTasks++ : blockedBoundary.pendingTasks++;
		null !== row && row.pendingTasks++;
		replay.pendingTasks++;
		var task = {
			replay,
			node,
			childIndex,
			ping: function() {
				return pingTask(request, task);
			},
			blockedBoundary,
			blockedSegment: null,
			blockedPreamble: null,
			hoistableState,
			abortSet,
			keyPath,
			formatContext,
			context,
			treeContext,
			row,
			componentStack,
			thenableState
		};
		abortSet.add(task);
		return task;
	}
	function createPendingSegment(request, index, boundary, parentFormatContext, lastPushedText, textEmbedded) {
		return {
			status: 0,
			parentFlushed: !1,
			id: -1,
			index,
			chunks: [],
			children: [],
			preambleChildren: [],
			parentFormatContext,
			boundary,
			lastPushedText,
			textEmbedded
		};
	}
	function pushComponentStack(task) {
		var node = task.node;
		if ("object" === typeof node && null !== node) switch (node.$$typeof) {
			case REACT_ELEMENT_TYPE: task.componentStack = {
				parent: task.componentStack,
				type: node.type
			};
		}
	}
	function replaceSuspenseComponentStackWithSuspenseFallbackStack(componentStack) {
		return null === componentStack ? null : {
			parent: componentStack.parent,
			type: "Suspense Fallback"
		};
	}
	function getThrownInfo(node$jscomp$0) {
		var errorInfo = {};
		node$jscomp$0 && Object.defineProperty(errorInfo, "componentStack", {
			configurable: !0,
			enumerable: !0,
			get: function() {
				try {
					var info = "", node = node$jscomp$0;
					do
						info += describeComponentStackByType(node.type), node = node.parent;
					while (node);
					var JSCompiler_inline_result = info;
				} catch (x) {
					JSCompiler_inline_result = "\nError generating stack: " + x.message + "\n" + x.stack;
				}
				Object.defineProperty(errorInfo, "componentStack", { value: JSCompiler_inline_result });
				return JSCompiler_inline_result;
			}
		});
		return errorInfo;
	}
	function logRecoverableError(request, error, errorInfo) {
		request = request.onError;
		error = request(error, errorInfo);
		if (null == error || "string" === typeof error) return error;
	}
	function fatalError(request, error) {
		var onShellError = request.onShellError, onFatalError = request.onFatalError;
		onShellError(error);
		onFatalError(error);
		null !== request.destination ? (request.status = 14, request.destination.destroy(error)) : (request.status = 13, request.fatalError = error);
	}
	function finishSuspenseListRow(request, row) {
		unblockSuspenseListRow(request, row.next, row.hoistables);
	}
	function unblockSuspenseListRow(request, unblockedRow, inheritedHoistables) {
		for (; null !== unblockedRow;) {
			null !== inheritedHoistables && (hoistHoistables(unblockedRow.hoistables, inheritedHoistables), unblockedRow.inheritedHoistables = inheritedHoistables);
			var unblockedBoundaries = unblockedRow.boundaries;
			if (null !== unblockedBoundaries) {
				unblockedRow.boundaries = null;
				for (var i = 0; i < unblockedBoundaries.length; i++) {
					var unblockedBoundary = unblockedBoundaries[i];
					null !== inheritedHoistables && hoistHoistables(unblockedBoundary.contentState, inheritedHoistables);
					finishedTask(request, unblockedBoundary, null, null);
				}
			}
			unblockedRow.pendingTasks--;
			if (0 < unblockedRow.pendingTasks) break;
			inheritedHoistables = unblockedRow.hoistables;
			unblockedRow = unblockedRow.next;
		}
	}
	function tryToResolveTogetherRow(request, togetherRow) {
		var boundaries = togetherRow.boundaries;
		if (null !== boundaries && togetherRow.pendingTasks === boundaries.length) {
			for (var allCompleteAndInlinable = !0, i = 0; i < boundaries.length; i++) {
				var rowBoundary = boundaries[i];
				if (1 !== rowBoundary.pendingTasks || rowBoundary.parentFlushed || isEligibleForOutlining(request, rowBoundary)) {
					allCompleteAndInlinable = !1;
					break;
				}
			}
			allCompleteAndInlinable && unblockSuspenseListRow(request, togetherRow, togetherRow.hoistables);
		}
	}
	function createSuspenseListRow(previousRow) {
		var newRow = {
			pendingTasks: 1,
			boundaries: null,
			hoistables: createHoistableState(),
			inheritedHoistables: null,
			together: !1,
			next: null
		};
		null !== previousRow && 0 < previousRow.pendingTasks && (newRow.pendingTasks++, newRow.boundaries = [], previousRow.next = newRow);
		return newRow;
	}
	function renderSuspenseListRows(request, task, keyPath, rows, revealOrder) {
		var prevKeyPath = task.keyPath, prevTreeContext = task.treeContext, prevRow = task.row;
		task.keyPath = keyPath;
		keyPath = rows.length;
		var previousSuspenseListRow = null;
		if (null !== task.replay) {
			var resumeSlots = task.replay.slots;
			if (null !== resumeSlots && "object" === typeof resumeSlots) for (var n = 0; n < keyPath; n++) {
				var i = "backwards" !== revealOrder && "unstable_legacy-backwards" !== revealOrder ? n : keyPath - 1 - n, node = rows[i];
				task.row = previousSuspenseListRow = createSuspenseListRow(previousSuspenseListRow);
				task.treeContext = pushTreeContext(prevTreeContext, keyPath, i);
				var resumeSegmentID = resumeSlots[i];
				"number" === typeof resumeSegmentID ? (resumeNode(request, task, resumeSegmentID, node, i), delete resumeSlots[i]) : renderNode(request, task, node, i);
				0 === --previousSuspenseListRow.pendingTasks && finishSuspenseListRow(request, previousSuspenseListRow);
			}
			else for (resumeSlots = 0; resumeSlots < keyPath; resumeSlots++) n = "backwards" !== revealOrder && "unstable_legacy-backwards" !== revealOrder ? resumeSlots : keyPath - 1 - resumeSlots, i = rows[n], task.row = previousSuspenseListRow = createSuspenseListRow(previousSuspenseListRow), task.treeContext = pushTreeContext(prevTreeContext, keyPath, n), renderNode(request, task, i, n), 0 === --previousSuspenseListRow.pendingTasks && finishSuspenseListRow(request, previousSuspenseListRow);
		} else if ("backwards" !== revealOrder && "unstable_legacy-backwards" !== revealOrder) for (revealOrder = 0; revealOrder < keyPath; revealOrder++) resumeSlots = rows[revealOrder], task.row = previousSuspenseListRow = createSuspenseListRow(previousSuspenseListRow), task.treeContext = pushTreeContext(prevTreeContext, keyPath, revealOrder), renderNode(request, task, resumeSlots, revealOrder), 0 === --previousSuspenseListRow.pendingTasks && finishSuspenseListRow(request, previousSuspenseListRow);
		else {
			revealOrder = task.blockedSegment;
			resumeSlots = revealOrder.children.length;
			n = revealOrder.chunks.length;
			for (i = keyPath - 1; 0 <= i; i--) {
				node = rows[i];
				task.row = previousSuspenseListRow = createSuspenseListRow(previousSuspenseListRow);
				task.treeContext = pushTreeContext(prevTreeContext, keyPath, i);
				resumeSegmentID = createPendingSegment(request, n, null, task.formatContext, 0 === i ? revealOrder.lastPushedText : !0, !0);
				revealOrder.children.splice(resumeSlots, 0, resumeSegmentID);
				task.blockedSegment = resumeSegmentID;
				try {
					renderNode(request, task, node, i), pushSegmentFinale(resumeSegmentID.chunks, request.renderState, resumeSegmentID.lastPushedText, resumeSegmentID.textEmbedded), resumeSegmentID.status = 1, 0 === --previousSuspenseListRow.pendingTasks && finishSuspenseListRow(request, previousSuspenseListRow);
				} catch (thrownValue) {
					throw resumeSegmentID.status = 12 === request.status ? 3 : 4, thrownValue;
				}
			}
			task.blockedSegment = revealOrder;
			revealOrder.lastPushedText = !1;
		}
		null !== prevRow && null !== previousSuspenseListRow && 0 < previousSuspenseListRow.pendingTasks && (prevRow.pendingTasks++, previousSuspenseListRow.next = prevRow);
		task.treeContext = prevTreeContext;
		task.row = prevRow;
		task.keyPath = prevKeyPath;
	}
	function renderWithHooks(request, task, keyPath, Component, props, secondArg) {
		var prevThenableState = task.thenableState;
		task.thenableState = null;
		currentlyRenderingComponent = {};
		currentlyRenderingTask = task;
		currentlyRenderingRequest = request;
		currentlyRenderingKeyPath = keyPath;
		actionStateCounter = localIdCounter = 0;
		actionStateMatchingIndex = -1;
		thenableIndexCounter = 0;
		thenableState = prevThenableState;
		for (request = Component(props, secondArg); didScheduleRenderPhaseUpdate;) didScheduleRenderPhaseUpdate = !1, actionStateCounter = localIdCounter = 0, actionStateMatchingIndex = -1, thenableIndexCounter = 0, numberOfReRenders += 1, workInProgressHook = null, request = Component(props, secondArg);
		resetHooksState();
		return request;
	}
	function finishFunctionComponent(request, task, keyPath, children, hasId, actionStateCount, actionStateMatchingIndex) {
		var didEmitActionStateMarkers = !1;
		if (0 !== actionStateCount && null !== request.formState) {
			var segment = task.blockedSegment;
			if (null !== segment) {
				didEmitActionStateMarkers = !0;
				segment = segment.chunks;
				for (var i = 0; i < actionStateCount; i++) i === actionStateMatchingIndex ? segment.push("<!--F!-->") : segment.push("<!--F-->");
			}
		}
		actionStateCount = task.keyPath;
		task.keyPath = keyPath;
		hasId ? (keyPath = task.treeContext, task.treeContext = pushTreeContext(keyPath, 1, 0), renderNode(request, task, children, -1), task.treeContext = keyPath) : didEmitActionStateMarkers ? renderNode(request, task, children, -1) : renderNodeDestructive(request, task, children, -1);
		task.keyPath = actionStateCount;
	}
	function renderElement(request, task, keyPath, type, props, ref) {
		if ("function" === typeof type) if (type.prototype && type.prototype.isReactComponent) {
			var newProps = props;
			if ("ref" in props) {
				newProps = {};
				for (var propName in props) "ref" !== propName && (newProps[propName] = props[propName]);
			}
			var defaultProps = type.defaultProps;
			if (defaultProps) {
				newProps === props && (newProps = assign({}, newProps, props));
				for (var propName$43 in defaultProps) void 0 === newProps[propName$43] && (newProps[propName$43] = defaultProps[propName$43]);
			}
			props = newProps;
			newProps = emptyContextObject;
			defaultProps = type.contextType;
			"object" === typeof defaultProps && null !== defaultProps && (newProps = defaultProps._currentValue2);
			newProps = new type(props, newProps);
			var initialState = void 0 !== newProps.state ? newProps.state : null;
			newProps.updater = classComponentUpdater;
			newProps.props = props;
			newProps.state = initialState;
			defaultProps = {
				queue: [],
				replace: !1
			};
			newProps._reactInternals = defaultProps;
			ref = type.contextType;
			newProps.context = "object" === typeof ref && null !== ref ? ref._currentValue2 : emptyContextObject;
			ref = type.getDerivedStateFromProps;
			"function" === typeof ref && (ref = ref(props, initialState), initialState = null === ref || void 0 === ref ? initialState : assign({}, initialState, ref), newProps.state = initialState);
			if ("function" !== typeof type.getDerivedStateFromProps && "function" !== typeof newProps.getSnapshotBeforeUpdate && ("function" === typeof newProps.UNSAFE_componentWillMount || "function" === typeof newProps.componentWillMount)) if (type = newProps.state, "function" === typeof newProps.componentWillMount && newProps.componentWillMount(), "function" === typeof newProps.UNSAFE_componentWillMount && newProps.UNSAFE_componentWillMount(), type !== newProps.state && classComponentUpdater.enqueueReplaceState(newProps, newProps.state, null), null !== defaultProps.queue && 0 < defaultProps.queue.length) if (type = defaultProps.queue, ref = defaultProps.replace, defaultProps.queue = null, defaultProps.replace = !1, ref && 1 === type.length) newProps.state = type[0];
			else {
				defaultProps = ref ? type[0] : newProps.state;
				initialState = !0;
				for (ref = ref ? 1 : 0; ref < type.length; ref++) propName$43 = type[ref], propName$43 = "function" === typeof propName$43 ? propName$43.call(newProps, defaultProps, props, void 0) : propName$43, null != propName$43 && (initialState ? (initialState = !1, defaultProps = assign({}, defaultProps, propName$43)) : assign(defaultProps, propName$43));
				newProps.state = defaultProps;
			}
			else defaultProps.queue = null;
			type = newProps.render();
			if (12 === request.status) throw null;
			props = task.keyPath;
			task.keyPath = keyPath;
			renderNodeDestructive(request, task, type, -1);
			task.keyPath = props;
		} else {
			type = renderWithHooks(request, task, keyPath, type, props, void 0);
			if (12 === request.status) throw null;
			finishFunctionComponent(request, task, keyPath, type, 0 !== localIdCounter, actionStateCounter, actionStateMatchingIndex);
		}
		else if ("string" === typeof type) if (newProps = task.blockedSegment, null === newProps) newProps = props.children, defaultProps = task.formatContext, initialState = task.keyPath, task.formatContext = getChildFormatContext(defaultProps, type, props), task.keyPath = keyPath, renderNode(request, task, newProps, -1), task.formatContext = defaultProps, task.keyPath = initialState;
		else {
			initialState = pushStartInstance(newProps.chunks, type, props, request.resumableState, request.renderState, task.blockedPreamble, task.hoistableState, task.formatContext, newProps.lastPushedText);
			newProps.lastPushedText = !1;
			defaultProps = task.formatContext;
			ref = task.keyPath;
			task.keyPath = keyPath;
			if (3 === (task.formatContext = getChildFormatContext(defaultProps, type, props)).insertionMode) {
				keyPath = createPendingSegment(request, 0, null, task.formatContext, !1, !1);
				newProps.preambleChildren.push(keyPath);
				task.blockedSegment = keyPath;
				try {
					keyPath.status = 6, renderNode(request, task, initialState, -1), pushSegmentFinale(keyPath.chunks, request.renderState, keyPath.lastPushedText, keyPath.textEmbedded), keyPath.status = 1;
				} finally {
					task.blockedSegment = newProps;
				}
			} else renderNode(request, task, initialState, -1);
			task.formatContext = defaultProps;
			task.keyPath = ref;
			a: {
				task = newProps.chunks;
				request = request.resumableState;
				switch (type) {
					case "title":
					case "style":
					case "script":
					case "area":
					case "base":
					case "br":
					case "col":
					case "embed":
					case "hr":
					case "img":
					case "input":
					case "keygen":
					case "link":
					case "meta":
					case "param":
					case "source":
					case "track":
					case "wbr": break a;
					case "body":
						if (1 >= defaultProps.insertionMode) {
							request.hasBody = !0;
							break a;
						}
						break;
					case "html":
						if (0 === defaultProps.insertionMode) {
							request.hasHtml = !0;
							break a;
						}
						break;
					case "head": if (1 >= defaultProps.insertionMode) break a;
				}
				task.push(endChunkForTag(type));
			}
			newProps.lastPushedText = !1;
		}
		else {
			switch (type) {
				case REACT_LEGACY_HIDDEN_TYPE:
				case REACT_STRICT_MODE_TYPE:
				case REACT_PROFILER_TYPE:
				case REACT_FRAGMENT_TYPE:
					type = task.keyPath;
					task.keyPath = keyPath;
					renderNodeDestructive(request, task, props.children, -1);
					task.keyPath = type;
					return;
				case REACT_ACTIVITY_TYPE:
					type = task.blockedSegment;
					null === type ? "hidden" !== props.mode && (type = task.keyPath, task.keyPath = keyPath, renderNode(request, task, props.children, -1), task.keyPath = type) : "hidden" !== props.mode && (request.renderState.generateStaticMarkup || type.chunks.push("<!--&-->"), type.lastPushedText = !1, newProps = task.keyPath, task.keyPath = keyPath, renderNode(request, task, props.children, -1), task.keyPath = newProps, request.renderState.generateStaticMarkup || type.chunks.push("<!--/&-->"), type.lastPushedText = !1);
					return;
				case REACT_SUSPENSE_LIST_TYPE:
					a: {
						type = props.children;
						props = props.revealOrder;
						if ("forwards" === props || "backwards" === props || "unstable_legacy-backwards" === props) {
							if (isArrayImpl(type)) {
								renderSuspenseListRows(request, task, keyPath, type, props);
								break a;
							}
							if (newProps = getIteratorFn(type)) {
								if (newProps = newProps.call(type)) {
									defaultProps = newProps.next();
									if (!defaultProps.done) {
										do
											defaultProps = newProps.next();
										while (!defaultProps.done);
										renderSuspenseListRows(request, task, keyPath, type, props);
									}
									break a;
								}
							}
						}
						"together" === props ? (props = task.keyPath, newProps = task.row, defaultProps = task.row = createSuspenseListRow(null), defaultProps.boundaries = [], defaultProps.together = !0, task.keyPath = keyPath, renderNodeDestructive(request, task, type, -1), 0 === --defaultProps.pendingTasks && finishSuspenseListRow(request, defaultProps), task.keyPath = props, task.row = newProps, null !== newProps && 0 < defaultProps.pendingTasks && (newProps.pendingTasks++, defaultProps.next = newProps)) : (props = task.keyPath, task.keyPath = keyPath, renderNodeDestructive(request, task, type, -1), task.keyPath = props);
					}
					return;
				case REACT_VIEW_TRANSITION_TYPE:
				case REACT_SCOPE_TYPE: throw Error(formatProdErrorMessage(343));
				case REACT_SUSPENSE_TYPE:
					a: if (null !== task.replay) {
						type = task.keyPath;
						newProps = task.formatContext;
						defaultProps = task.row;
						task.keyPath = keyPath;
						task.formatContext = getSuspenseContentFormatContext(request.resumableState, newProps);
						task.row = null;
						keyPath = props.children;
						try {
							renderNode(request, task, keyPath, -1);
						} finally {
							task.keyPath = type, task.formatContext = newProps, task.row = defaultProps;
						}
					} else {
						type = task.keyPath;
						ref = task.formatContext;
						var prevRow = task.row, parentBoundary = task.blockedBoundary;
						propName$43 = task.blockedPreamble;
						var parentHoistableState = task.hoistableState;
						propName = task.blockedSegment;
						var fallback = props.fallback;
						props = props.children;
						var fallbackAbortSet = /* @__PURE__ */ new Set();
						var newBoundary = createSuspenseBoundary(request, task.row, fallbackAbortSet, null, null);
						null !== request.trackedPostpones && (newBoundary.trackedContentKeyPath = keyPath);
						var boundarySegment = createPendingSegment(request, propName.chunks.length, newBoundary, task.formatContext, !1, !1);
						propName.children.push(boundarySegment);
						propName.lastPushedText = !1;
						var contentRootSegment = createPendingSegment(request, 0, null, task.formatContext, !1, !1);
						contentRootSegment.parentFlushed = !0;
						if (null !== request.trackedPostpones) {
							newProps = task.componentStack;
							defaultProps = [
								keyPath[0],
								"Suspense Fallback",
								keyPath[2]
							];
							initialState = [
								defaultProps[1],
								defaultProps[2],
								[],
								null
							];
							request.trackedPostpones.workingMap.set(defaultProps, initialState);
							newBoundary.trackedFallbackNode = initialState;
							task.blockedSegment = boundarySegment;
							task.blockedPreamble = newBoundary.fallbackPreamble;
							task.keyPath = defaultProps;
							task.formatContext = getSuspenseFallbackFormatContext(request.resumableState, ref);
							task.componentStack = replaceSuspenseComponentStackWithSuspenseFallbackStack(newProps);
							boundarySegment.status = 6;
							try {
								renderNode(request, task, fallback, -1), pushSegmentFinale(boundarySegment.chunks, request.renderState, boundarySegment.lastPushedText, boundarySegment.textEmbedded), boundarySegment.status = 1;
							} catch (thrownValue) {
								throw boundarySegment.status = 12 === request.status ? 3 : 4, thrownValue;
							} finally {
								task.blockedSegment = propName, task.blockedPreamble = propName$43, task.keyPath = type, task.formatContext = ref;
							}
							task = createRenderTask(request, null, props, -1, newBoundary, contentRootSegment, newBoundary.contentPreamble, newBoundary.contentState, task.abortSet, keyPath, getSuspenseContentFormatContext(request.resumableState, task.formatContext), task.context, task.treeContext, null, newProps);
							pushComponentStack(task);
							request.pingedTasks.push(task);
						} else {
							task.blockedBoundary = newBoundary;
							task.blockedPreamble = newBoundary.contentPreamble;
							task.hoistableState = newBoundary.contentState;
							task.blockedSegment = contentRootSegment;
							task.keyPath = keyPath;
							task.formatContext = getSuspenseContentFormatContext(request.resumableState, ref);
							task.row = null;
							contentRootSegment.status = 6;
							try {
								if (renderNode(request, task, props, -1), pushSegmentFinale(contentRootSegment.chunks, request.renderState, contentRootSegment.lastPushedText, contentRootSegment.textEmbedded), contentRootSegment.status = 1, queueCompletedSegment(newBoundary, contentRootSegment), 0 === newBoundary.pendingTasks && 0 === newBoundary.status) {
									if (newBoundary.status = 1, !isEligibleForOutlining(request, newBoundary)) {
										null !== prevRow && 0 === --prevRow.pendingTasks && finishSuspenseListRow(request, prevRow);
										0 === request.pendingRootTasks && task.blockedPreamble && preparePreamble(request);
										break a;
									}
								} else null !== prevRow && prevRow.together && tryToResolveTogetherRow(request, prevRow);
							} catch (thrownValue$30) {
								newBoundary.status = 4, 12 === request.status ? (contentRootSegment.status = 3, newProps = request.fatalError) : (contentRootSegment.status = 4, newProps = thrownValue$30), defaultProps = getThrownInfo(task.componentStack), initialState = logRecoverableError(request, newProps, defaultProps), newBoundary.errorDigest = initialState, untrackBoundary(request, newBoundary);
							} finally {
								task.blockedBoundary = parentBoundary, task.blockedPreamble = propName$43, task.hoistableState = parentHoistableState, task.blockedSegment = propName, task.keyPath = type, task.formatContext = ref, task.row = prevRow;
							}
							task = createRenderTask(request, null, fallback, -1, parentBoundary, boundarySegment, newBoundary.fallbackPreamble, newBoundary.fallbackState, fallbackAbortSet, [
								keyPath[0],
								"Suspense Fallback",
								keyPath[2]
							], getSuspenseFallbackFormatContext(request.resumableState, task.formatContext), task.context, task.treeContext, task.row, replaceSuspenseComponentStackWithSuspenseFallbackStack(task.componentStack));
							pushComponentStack(task);
							request.pingedTasks.push(task);
						}
					}
					return;
			}
			if ("object" === typeof type && null !== type) switch (type.$$typeof) {
				case REACT_FORWARD_REF_TYPE:
					if ("ref" in props) for (fallback in newProps = {}, props) "ref" !== fallback && (newProps[fallback] = props[fallback]);
					else newProps = props;
					type = renderWithHooks(request, task, keyPath, type.render, newProps, ref);
					finishFunctionComponent(request, task, keyPath, type, 0 !== localIdCounter, actionStateCounter, actionStateMatchingIndex);
					return;
				case REACT_MEMO_TYPE:
					renderElement(request, task, keyPath, type.type, props, ref);
					return;
				case REACT_CONTEXT_TYPE:
					defaultProps = props.children;
					newProps = task.keyPath;
					props = props.value;
					initialState = type._currentValue2;
					type._currentValue2 = props;
					ref = currentActiveSnapshot;
					currentActiveSnapshot = type = {
						parent: ref,
						depth: null === ref ? 0 : ref.depth + 1,
						context: type,
						parentValue: initialState,
						value: props
					};
					task.context = type;
					task.keyPath = keyPath;
					renderNodeDestructive(request, task, defaultProps, -1);
					request = currentActiveSnapshot;
					if (null === request) throw Error(formatProdErrorMessage(403));
					request.context._currentValue2 = request.parentValue;
					request = currentActiveSnapshot = request.parent;
					task.context = request;
					task.keyPath = newProps;
					return;
				case REACT_CONSUMER_TYPE:
					props = props.children;
					type = props(type._context._currentValue2);
					props = task.keyPath;
					task.keyPath = keyPath;
					renderNodeDestructive(request, task, type, -1);
					task.keyPath = props;
					return;
				case REACT_LAZY_TYPE:
					newProps = type._init;
					type = newProps(type._payload);
					if (12 === request.status) throw null;
					renderElement(request, task, keyPath, type, props, ref);
					return;
			}
			throw Error(formatProdErrorMessage(130, null == type ? type : typeof type, ""));
		}
	}
	function resumeNode(request, task, segmentId, node, childIndex) {
		var prevReplay = task.replay, blockedBoundary = task.blockedBoundary, resumedSegment = createPendingSegment(request, 0, null, task.formatContext, !1, !1);
		resumedSegment.id = segmentId;
		resumedSegment.parentFlushed = !0;
		try {
			task.replay = null, task.blockedSegment = resumedSegment, renderNode(request, task, node, childIndex), resumedSegment.status = 1, null === blockedBoundary ? request.completedRootSegment = resumedSegment : (queueCompletedSegment(blockedBoundary, resumedSegment), blockedBoundary.parentFlushed && request.partialBoundaries.push(blockedBoundary));
		} finally {
			task.replay = prevReplay, task.blockedSegment = null;
		}
	}
	function renderNodeDestructive(request, task, node, childIndex) {
		null !== task.replay && "number" === typeof task.replay.slots ? resumeNode(request, task, task.replay.slots, node, childIndex) : (task.node = node, task.childIndex = childIndex, node = task.componentStack, pushComponentStack(task), retryNode(request, task), task.componentStack = node);
	}
	function retryNode(request, task) {
		var node = task.node, childIndex = task.childIndex;
		if (null !== node) {
			if ("object" === typeof node) {
				switch (node.$$typeof) {
					case REACT_ELEMENT_TYPE:
						var type = node.type, key = node.key, props = node.props;
						node = props.ref;
						var ref = void 0 !== node ? node : null, name = getComponentNameFromType(type), keyOrIndex = null == key ? -1 === childIndex ? 0 : childIndex : key;
						key = [
							task.keyPath,
							name,
							keyOrIndex
						];
						if (null !== task.replay) a: {
							var replay = task.replay;
							childIndex = replay.nodes;
							for (node = 0; node < childIndex.length; node++) {
								var node$jscomp$0 = childIndex[node];
								if (keyOrIndex === node$jscomp$0[1]) {
									if (4 === node$jscomp$0.length) {
										if (null !== name && name !== node$jscomp$0[0]) throw Error(formatProdErrorMessage(490, node$jscomp$0[0], name));
										var childNodes = node$jscomp$0[2];
										name = node$jscomp$0[3];
										keyOrIndex = task.node;
										task.replay = {
											nodes: childNodes,
											slots: name,
											pendingTasks: 1
										};
										try {
											renderElement(request, task, key, type, props, ref);
											if (1 === task.replay.pendingTasks && 0 < task.replay.nodes.length) throw Error(formatProdErrorMessage(488));
											task.replay.pendingTasks--;
										} catch (x) {
											if ("object" === typeof x && null !== x && (x === SuspenseException || "function" === typeof x.then)) throw task.node === keyOrIndex ? task.replay = replay : childIndex.splice(node, 1), x;
											task.replay.pendingTasks--;
											props = getThrownInfo(task.componentStack);
											key = request;
											request = task.blockedBoundary;
											type = x;
											props = logRecoverableError(key, type, props);
											abortRemainingReplayNodes(key, request, childNodes, name, type, props);
										}
										task.replay = replay;
									} else {
										if (type !== REACT_SUSPENSE_TYPE) throw Error(formatProdErrorMessage(490, "Suspense", getComponentNameFromType(type) || "Unknown"));
										b: {
											replay = void 0;
											type = node$jscomp$0[5];
											ref = node$jscomp$0[2];
											name = node$jscomp$0[3];
											keyOrIndex = null === node$jscomp$0[4] ? [] : node$jscomp$0[4][2];
											node$jscomp$0 = null === node$jscomp$0[4] ? null : node$jscomp$0[4][3];
											var prevKeyPath = task.keyPath, prevContext = task.formatContext, prevRow = task.row, previousReplaySet = task.replay, parentBoundary = task.blockedBoundary, parentHoistableState = task.hoistableState, content = props.children, fallback = props.fallback, fallbackAbortSet = /* @__PURE__ */ new Set();
											props = createSuspenseBoundary(request, task.row, fallbackAbortSet, null, null);
											props.parentFlushed = !0;
											props.rootSegmentID = type;
											task.blockedBoundary = props;
											task.hoistableState = props.contentState;
											task.keyPath = key;
											task.formatContext = getSuspenseContentFormatContext(request.resumableState, prevContext);
											task.row = null;
											task.replay = {
												nodes: ref,
												slots: name,
												pendingTasks: 1
											};
											try {
												renderNode(request, task, content, -1);
												if (1 === task.replay.pendingTasks && 0 < task.replay.nodes.length) throw Error(formatProdErrorMessage(488));
												task.replay.pendingTasks--;
												if (0 === props.pendingTasks && 0 === props.status) {
													props.status = 1;
													request.completedBoundaries.push(props);
													break b;
												}
											} catch (error) {
												props.status = 4, childNodes = getThrownInfo(task.componentStack), replay = logRecoverableError(request, error, childNodes), props.errorDigest = replay, task.replay.pendingTasks--, request.clientRenderedBoundaries.push(props);
											} finally {
												task.blockedBoundary = parentBoundary, task.hoistableState = parentHoistableState, task.replay = previousReplaySet, task.keyPath = prevKeyPath, task.formatContext = prevContext, task.row = prevRow;
											}
											childNodes = createReplayTask(request, null, {
												nodes: keyOrIndex,
												slots: node$jscomp$0,
												pendingTasks: 0
											}, fallback, -1, parentBoundary, props.fallbackState, fallbackAbortSet, [
												key[0],
												"Suspense Fallback",
												key[2]
											], getSuspenseFallbackFormatContext(request.resumableState, task.formatContext), task.context, task.treeContext, task.row, replaceSuspenseComponentStackWithSuspenseFallbackStack(task.componentStack));
											pushComponentStack(childNodes);
											request.pingedTasks.push(childNodes);
										}
									}
									childIndex.splice(node, 1);
									break a;
								}
							}
						}
						else renderElement(request, task, key, type, props, ref);
						return;
					case REACT_PORTAL_TYPE: throw Error(formatProdErrorMessage(257));
					case REACT_LAZY_TYPE:
						childNodes = node._init;
						node = childNodes(node._payload);
						if (12 === request.status) throw null;
						renderNodeDestructive(request, task, node, childIndex);
						return;
				}
				if (isArrayImpl(node)) {
					renderChildrenArray(request, task, node, childIndex);
					return;
				}
				if (childNodes = getIteratorFn(node)) {
					if (childNodes = childNodes.call(node)) {
						node = childNodes.next();
						if (!node.done) {
							props = [];
							do
								props.push(node.value), node = childNodes.next();
							while (!node.done);
							renderChildrenArray(request, task, props, childIndex);
						}
						return;
					}
				}
				if ("function" === typeof node.then) return task.thenableState = null, renderNodeDestructive(request, task, unwrapThenable(node), childIndex);
				if (node.$$typeof === REACT_CONTEXT_TYPE) return renderNodeDestructive(request, task, node._currentValue2, childIndex);
				childIndex = Object.prototype.toString.call(node);
				throw Error(formatProdErrorMessage(31, "[object Object]" === childIndex ? "object with keys {" + Object.keys(node).join(", ") + "}" : childIndex));
			}
			if ("string" === typeof node) childIndex = task.blockedSegment, null !== childIndex && (childIndex.lastPushedText = pushTextInstance(childIndex.chunks, node, request.renderState, childIndex.lastPushedText));
			else if ("number" === typeof node || "bigint" === typeof node) childIndex = task.blockedSegment, null !== childIndex && (childIndex.lastPushedText = pushTextInstance(childIndex.chunks, "" + node, request.renderState, childIndex.lastPushedText));
		}
	}
	function renderChildrenArray(request, task, children, childIndex) {
		var prevKeyPath = task.keyPath;
		if (-1 !== childIndex && (task.keyPath = [
			task.keyPath,
			"Fragment",
			childIndex
		], null !== task.replay)) {
			for (var replay = task.replay, replayNodes = replay.nodes, j = 0; j < replayNodes.length; j++) {
				var node = replayNodes[j];
				if (node[1] === childIndex) {
					childIndex = node[2];
					node = node[3];
					task.replay = {
						nodes: childIndex,
						slots: node,
						pendingTasks: 1
					};
					try {
						renderChildrenArray(request, task, children, -1);
						if (1 === task.replay.pendingTasks && 0 < task.replay.nodes.length) throw Error(formatProdErrorMessage(488));
						task.replay.pendingTasks--;
					} catch (x) {
						if ("object" === typeof x && null !== x && (x === SuspenseException || "function" === typeof x.then)) throw x;
						task.replay.pendingTasks--;
						children = getThrownInfo(task.componentStack);
						var boundary = task.blockedBoundary, error = x;
						children = logRecoverableError(request, error, children);
						abortRemainingReplayNodes(request, boundary, childIndex, node, error, children);
					}
					task.replay = replay;
					replayNodes.splice(j, 1);
					break;
				}
			}
			task.keyPath = prevKeyPath;
			return;
		}
		replay = task.treeContext;
		replayNodes = children.length;
		if (null !== task.replay && (j = task.replay.slots, null !== j && "object" === typeof j)) {
			for (childIndex = 0; childIndex < replayNodes; childIndex++) node = children[childIndex], task.treeContext = pushTreeContext(replay, replayNodes, childIndex), boundary = j[childIndex], "number" === typeof boundary ? (resumeNode(request, task, boundary, node, childIndex), delete j[childIndex]) : renderNode(request, task, node, childIndex);
			task.treeContext = replay;
			task.keyPath = prevKeyPath;
			return;
		}
		for (j = 0; j < replayNodes; j++) childIndex = children[j], task.treeContext = pushTreeContext(replay, replayNodes, j), renderNode(request, task, childIndex, j);
		task.treeContext = replay;
		task.keyPath = prevKeyPath;
	}
	function trackPostponedBoundary(request, trackedPostpones, boundary) {
		boundary.status = 5;
		boundary.rootSegmentID = request.nextSegmentId++;
		request = boundary.trackedContentKeyPath;
		if (null === request) throw Error(formatProdErrorMessage(486));
		var fallbackReplayNode = boundary.trackedFallbackNode, children = [], boundaryNode = trackedPostpones.workingMap.get(request);
		if (void 0 === boundaryNode) return boundary = [
			request[1],
			request[2],
			children,
			null,
			fallbackReplayNode,
			boundary.rootSegmentID
		], trackedPostpones.workingMap.set(request, boundary), addToReplayParent(boundary, request[0], trackedPostpones), boundary;
		boundaryNode[4] = fallbackReplayNode;
		boundaryNode[5] = boundary.rootSegmentID;
		return boundaryNode;
	}
	function trackPostpone(request, trackedPostpones, task, segment) {
		segment.status = 5;
		var keyPath = task.keyPath, boundary = task.blockedBoundary;
		if (null === boundary) segment.id = request.nextSegmentId++, trackedPostpones.rootSlots = segment.id, null !== request.completedRootSegment && (request.completedRootSegment.status = 5);
		else {
			if (null !== boundary && 0 === boundary.status) {
				var boundaryNode = trackPostponedBoundary(request, trackedPostpones, boundary);
				if (boundary.trackedContentKeyPath === keyPath && -1 === task.childIndex) {
					-1 === segment.id && (segment.id = segment.parentFlushed ? boundary.rootSegmentID : request.nextSegmentId++);
					boundaryNode[3] = segment.id;
					return;
				}
			}
			-1 === segment.id && (segment.id = segment.parentFlushed && null !== boundary ? boundary.rootSegmentID : request.nextSegmentId++);
			if (-1 === task.childIndex) null === keyPath ? trackedPostpones.rootSlots = segment.id : (task = trackedPostpones.workingMap.get(keyPath), void 0 === task ? (task = [
				keyPath[1],
				keyPath[2],
				[],
				segment.id
			], addToReplayParent(task, keyPath[0], trackedPostpones)) : task[3] = segment.id);
			else {
				if (null === keyPath) {
					if (request = trackedPostpones.rootSlots, null === request) request = trackedPostpones.rootSlots = {};
					else if ("number" === typeof request) throw Error(formatProdErrorMessage(491));
				} else if (boundary = trackedPostpones.workingMap, boundaryNode = boundary.get(keyPath), void 0 === boundaryNode) request = {}, boundaryNode = [
					keyPath[1],
					keyPath[2],
					[],
					request
				], boundary.set(keyPath, boundaryNode), addToReplayParent(boundaryNode, keyPath[0], trackedPostpones);
				else if (request = boundaryNode[3], null === request) request = boundaryNode[3] = {};
				else if ("number" === typeof request) throw Error(formatProdErrorMessage(491));
				request[task.childIndex] = segment.id;
			}
		}
	}
	function untrackBoundary(request, boundary) {
		request = request.trackedPostpones;
		null !== request && (boundary = boundary.trackedContentKeyPath, null !== boundary && (boundary = request.workingMap.get(boundary), void 0 !== boundary && (boundary.length = 4, boundary[2] = [], boundary[3] = null)));
	}
	function spawnNewSuspendedReplayTask(request, task, thenableState) {
		return createReplayTask(request, thenableState, task.replay, task.node, task.childIndex, task.blockedBoundary, task.hoistableState, task.abortSet, task.keyPath, task.formatContext, task.context, task.treeContext, task.row, task.componentStack);
	}
	function spawnNewSuspendedRenderTask(request, task, thenableState) {
		var segment = task.blockedSegment, newSegment = createPendingSegment(request, segment.chunks.length, null, task.formatContext, segment.lastPushedText, !0);
		segment.children.push(newSegment);
		segment.lastPushedText = !1;
		return createRenderTask(request, thenableState, task.node, task.childIndex, task.blockedBoundary, newSegment, task.blockedPreamble, task.hoistableState, task.abortSet, task.keyPath, task.formatContext, task.context, task.treeContext, task.row, task.componentStack);
	}
	function renderNode(request, task, node, childIndex) {
		var previousFormatContext = task.formatContext, previousContext = task.context, previousKeyPath = task.keyPath, previousTreeContext = task.treeContext, previousComponentStack = task.componentStack, segment = task.blockedSegment;
		if (null === segment) {
			segment = task.replay;
			try {
				return renderNodeDestructive(request, task, node, childIndex);
			} catch (thrownValue) {
				if (resetHooksState(), node = thrownValue === SuspenseException ? getSuspendedThenable() : thrownValue, 12 !== request.status && "object" === typeof node && null !== node) {
					if ("function" === typeof node.then) {
						childIndex = thrownValue === SuspenseException ? getThenableStateAfterSuspending() : null;
						request = spawnNewSuspendedReplayTask(request, task, childIndex).ping;
						node.then(request, request);
						task.formatContext = previousFormatContext;
						task.context = previousContext;
						task.keyPath = previousKeyPath;
						task.treeContext = previousTreeContext;
						task.componentStack = previousComponentStack;
						task.replay = segment;
						switchContext(previousContext);
						return;
					}
					if ("Maximum call stack size exceeded" === node.message) {
						node = thrownValue === SuspenseException ? getThenableStateAfterSuspending() : null;
						node = spawnNewSuspendedReplayTask(request, task, node);
						request.pingedTasks.push(node);
						task.formatContext = previousFormatContext;
						task.context = previousContext;
						task.keyPath = previousKeyPath;
						task.treeContext = previousTreeContext;
						task.componentStack = previousComponentStack;
						task.replay = segment;
						switchContext(previousContext);
						return;
					}
				}
			}
		} else {
			var childrenLength = segment.children.length, chunkLength = segment.chunks.length;
			try {
				return renderNodeDestructive(request, task, node, childIndex);
			} catch (thrownValue$62) {
				if (resetHooksState(), segment.children.length = childrenLength, segment.chunks.length = chunkLength, node = thrownValue$62 === SuspenseException ? getSuspendedThenable() : thrownValue$62, 12 !== request.status && "object" === typeof node && null !== node) {
					if ("function" === typeof node.then) {
						segment = node;
						node = thrownValue$62 === SuspenseException ? getThenableStateAfterSuspending() : null;
						request = spawnNewSuspendedRenderTask(request, task, node).ping;
						segment.then(request, request);
						task.formatContext = previousFormatContext;
						task.context = previousContext;
						task.keyPath = previousKeyPath;
						task.treeContext = previousTreeContext;
						task.componentStack = previousComponentStack;
						switchContext(previousContext);
						return;
					}
					if ("Maximum call stack size exceeded" === node.message) {
						segment = thrownValue$62 === SuspenseException ? getThenableStateAfterSuspending() : null;
						segment = spawnNewSuspendedRenderTask(request, task, segment);
						request.pingedTasks.push(segment);
						task.formatContext = previousFormatContext;
						task.context = previousContext;
						task.keyPath = previousKeyPath;
						task.treeContext = previousTreeContext;
						task.componentStack = previousComponentStack;
						switchContext(previousContext);
						return;
					}
				}
			}
		}
		task.formatContext = previousFormatContext;
		task.context = previousContext;
		task.keyPath = previousKeyPath;
		task.treeContext = previousTreeContext;
		switchContext(previousContext);
		throw node;
	}
	function abortTaskSoft(task) {
		var boundary = task.blockedBoundary, segment = task.blockedSegment;
		null !== segment && (segment.status = 3, finishedTask(this, boundary, task.row, segment));
	}
	function abortRemainingReplayNodes(request$jscomp$0, boundary, nodes, slots, error, errorDigest$jscomp$0) {
		for (var i = 0; i < nodes.length; i++) {
			var node = nodes[i];
			if (4 === node.length) abortRemainingReplayNodes(request$jscomp$0, boundary, node[2], node[3], error, errorDigest$jscomp$0);
			else {
				node = node[5];
				var request = request$jscomp$0, errorDigest = errorDigest$jscomp$0, resumedBoundary = createSuspenseBoundary(request, null, /* @__PURE__ */ new Set(), null, null);
				resumedBoundary.parentFlushed = !0;
				resumedBoundary.rootSegmentID = node;
				resumedBoundary.status = 4;
				resumedBoundary.errorDigest = errorDigest;
				resumedBoundary.parentFlushed && request.clientRenderedBoundaries.push(resumedBoundary);
			}
		}
		nodes.length = 0;
		if (null !== slots) {
			if (null === boundary) throw Error(formatProdErrorMessage(487));
			4 !== boundary.status && (boundary.status = 4, boundary.errorDigest = errorDigest$jscomp$0, boundary.parentFlushed && request$jscomp$0.clientRenderedBoundaries.push(boundary));
			if ("object" === typeof slots) for (var index in slots) delete slots[index];
		}
	}
	function abortTask(task, request, error) {
		var boundary = task.blockedBoundary, segment = task.blockedSegment;
		if (null !== segment) {
			if (6 === segment.status) return;
			segment.status = 3;
		}
		var errorInfo = getThrownInfo(task.componentStack);
		if (null === boundary) {
			if (13 !== request.status && 14 !== request.status) {
				boundary = task.replay;
				if (null === boundary) {
					null !== request.trackedPostpones && null !== segment ? (boundary = request.trackedPostpones, logRecoverableError(request, error, errorInfo), trackPostpone(request, boundary, task, segment), finishedTask(request, null, task.row, segment)) : (logRecoverableError(request, error, errorInfo), fatalError(request, error));
					return;
				}
				boundary.pendingTasks--;
				0 === boundary.pendingTasks && 0 < boundary.nodes.length && (segment = logRecoverableError(request, error, errorInfo), abortRemainingReplayNodes(request, null, boundary.nodes, boundary.slots, error, segment));
				request.pendingRootTasks--;
				0 === request.pendingRootTasks && completeShell(request);
			}
		} else {
			var trackedPostpones$63 = request.trackedPostpones;
			if (4 !== boundary.status) {
				if (null !== trackedPostpones$63 && null !== segment) return logRecoverableError(request, error, errorInfo), trackPostpone(request, trackedPostpones$63, task, segment), boundary.fallbackAbortableTasks.forEach(function(fallbackTask) {
					return abortTask(fallbackTask, request, error);
				}), boundary.fallbackAbortableTasks.clear(), finishedTask(request, boundary, task.row, segment);
				boundary.status = 4;
				segment = logRecoverableError(request, error, errorInfo);
				boundary.status = 4;
				boundary.errorDigest = segment;
				untrackBoundary(request, boundary);
				boundary.parentFlushed && request.clientRenderedBoundaries.push(boundary);
			}
			boundary.pendingTasks--;
			segment = boundary.row;
			null !== segment && 0 === --segment.pendingTasks && finishSuspenseListRow(request, segment);
			boundary.fallbackAbortableTasks.forEach(function(fallbackTask) {
				return abortTask(fallbackTask, request, error);
			});
			boundary.fallbackAbortableTasks.clear();
		}
		task = task.row;
		null !== task && 0 === --task.pendingTasks && finishSuspenseListRow(request, task);
		request.allPendingTasks--;
		0 === request.allPendingTasks && completeAll(request);
	}
	function safelyEmitEarlyPreloads(request, shellComplete) {
		try {
			var renderState = request.renderState, onHeaders = renderState.onHeaders;
			if (onHeaders) {
				var headers = renderState.headers;
				if (headers) {
					renderState.headers = null;
					var linkHeader = headers.preconnects;
					headers.fontPreloads && (linkHeader && (linkHeader += ", "), linkHeader += headers.fontPreloads);
					headers.highImagePreloads && (linkHeader && (linkHeader += ", "), linkHeader += headers.highImagePreloads);
					if (!shellComplete) {
						var queueIter = renderState.styles.values(), queueStep = queueIter.next();
						b: for (; 0 < headers.remainingCapacity && !queueStep.done; queueStep = queueIter.next()) for (var sheetIter = queueStep.value.sheets.values(), sheetStep = sheetIter.next(); 0 < headers.remainingCapacity && !sheetStep.done; sheetStep = sheetIter.next()) {
							var sheet = sheetStep.value, props = sheet.props, key = props.href, props$jscomp$0 = sheet.props, header = getPreloadAsHeader(props$jscomp$0.href, "style", {
								crossOrigin: props$jscomp$0.crossOrigin,
								integrity: props$jscomp$0.integrity,
								nonce: props$jscomp$0.nonce,
								type: props$jscomp$0.type,
								fetchPriority: props$jscomp$0.fetchPriority,
								referrerPolicy: props$jscomp$0.referrerPolicy,
								media: props$jscomp$0.media
							});
							if (0 <= (headers.remainingCapacity -= header.length + 2)) renderState.resets.style[key] = PRELOAD_NO_CREDS, linkHeader && (linkHeader += ", "), linkHeader += header, renderState.resets.style[key] = "string" === typeof props.crossOrigin || "string" === typeof props.integrity ? [props.crossOrigin, props.integrity] : PRELOAD_NO_CREDS;
							else break b;
						}
					}
					linkHeader ? onHeaders({ Link: linkHeader }) : onHeaders({});
				}
			}
		} catch (error) {
			logRecoverableError(request, error, {});
		}
	}
	function completeShell(request) {
		null === request.trackedPostpones && safelyEmitEarlyPreloads(request, !0);
		null === request.trackedPostpones && preparePreamble(request);
		request.onShellError = noop;
		request = request.onShellReady;
		request();
	}
	function completeAll(request) {
		safelyEmitEarlyPreloads(request, null === request.trackedPostpones ? !0 : null === request.completedRootSegment || 5 !== request.completedRootSegment.status);
		preparePreamble(request);
		request = request.onAllReady;
		request();
	}
	function queueCompletedSegment(boundary, segment) {
		if (0 === segment.chunks.length && 1 === segment.children.length && null === segment.children[0].boundary && -1 === segment.children[0].id) {
			var childSegment = segment.children[0];
			childSegment.id = segment.id;
			childSegment.parentFlushed = !0;
			1 !== childSegment.status && 3 !== childSegment.status && 4 !== childSegment.status || queueCompletedSegment(boundary, childSegment);
		} else boundary.completedSegments.push(segment);
	}
	function finishedTask(request, boundary, row, segment) {
		null !== row && (0 === --row.pendingTasks ? finishSuspenseListRow(request, row) : row.together && tryToResolveTogetherRow(request, row));
		request.allPendingTasks--;
		if (null === boundary) {
			if (null !== segment && segment.parentFlushed) {
				if (null !== request.completedRootSegment) throw Error(formatProdErrorMessage(389));
				request.completedRootSegment = segment;
			}
			request.pendingRootTasks--;
			0 === request.pendingRootTasks && completeShell(request);
		} else if (boundary.pendingTasks--, 4 !== boundary.status) if (0 === boundary.pendingTasks) {
			if (0 === boundary.status && (boundary.status = 1), null !== segment && segment.parentFlushed && (1 === segment.status || 3 === segment.status) && queueCompletedSegment(boundary, segment), boundary.parentFlushed && request.completedBoundaries.push(boundary), 1 === boundary.status) row = boundary.row, null !== row && hoistHoistables(row.hoistables, boundary.contentState), isEligibleForOutlining(request, boundary) || (boundary.fallbackAbortableTasks.forEach(abortTaskSoft, request), boundary.fallbackAbortableTasks.clear(), null !== row && 0 === --row.pendingTasks && finishSuspenseListRow(request, row)), 0 === request.pendingRootTasks && null === request.trackedPostpones && null !== boundary.contentPreamble && preparePreamble(request);
			else if (5 === boundary.status && (boundary = boundary.row, null !== boundary)) {
				if (null !== request.trackedPostpones) {
					row = request.trackedPostpones;
					var postponedRow = boundary.next;
					if (null !== postponedRow && (segment = postponedRow.boundaries, null !== segment)) for (postponedRow.boundaries = null, postponedRow = 0; postponedRow < segment.length; postponedRow++) {
						var postponedBoundary = segment[postponedRow];
						trackPostponedBoundary(request, row, postponedBoundary);
						finishedTask(request, postponedBoundary, null, null);
					}
				}
				0 === --boundary.pendingTasks && finishSuspenseListRow(request, boundary);
			}
		} else null === segment || !segment.parentFlushed || 1 !== segment.status && 3 !== segment.status || (queueCompletedSegment(boundary, segment), 1 === boundary.completedSegments.length && boundary.parentFlushed && request.partialBoundaries.push(boundary)), boundary = boundary.row, null !== boundary && boundary.together && tryToResolveTogetherRow(request, boundary);
		0 === request.allPendingTasks && completeAll(request);
	}
	function performWork(request$jscomp$2) {
		if (14 !== request$jscomp$2.status && 13 !== request$jscomp$2.status) {
			var prevContext = currentActiveSnapshot, prevDispatcher = ReactSharedInternals.H;
			ReactSharedInternals.H = HooksDispatcher;
			var prevAsyncDispatcher = ReactSharedInternals.A;
			ReactSharedInternals.A = DefaultAsyncDispatcher;
			var prevRequest = currentRequest;
			currentRequest = request$jscomp$2;
			var prevResumableState = currentResumableState;
			currentResumableState = request$jscomp$2.resumableState;
			try {
				var pingedTasks = request$jscomp$2.pingedTasks, i;
				for (i = 0; i < pingedTasks.length; i++) {
					var task = pingedTasks[i], request = request$jscomp$2, segment = task.blockedSegment;
					if (null === segment) {
						var request$jscomp$0 = request;
						if (0 !== task.replay.pendingTasks) {
							switchContext(task.context);
							try {
								"number" === typeof task.replay.slots ? resumeNode(request$jscomp$0, task, task.replay.slots, task.node, task.childIndex) : retryNode(request$jscomp$0, task);
								if (1 === task.replay.pendingTasks && 0 < task.replay.nodes.length) throw Error(formatProdErrorMessage(488));
								task.replay.pendingTasks--;
								task.abortSet.delete(task);
								finishedTask(request$jscomp$0, task.blockedBoundary, task.row, null);
							} catch (thrownValue) {
								resetHooksState();
								var x = thrownValue === SuspenseException ? getSuspendedThenable() : thrownValue;
								if ("object" === typeof x && null !== x && "function" === typeof x.then) {
									var ping = task.ping;
									x.then(ping, ping);
									task.thenableState = thrownValue === SuspenseException ? getThenableStateAfterSuspending() : null;
								} else {
									task.replay.pendingTasks--;
									task.abortSet.delete(task);
									var errorInfo = getThrownInfo(task.componentStack);
									request = void 0;
									var request$jscomp$1 = request$jscomp$0, boundary = task.blockedBoundary, error$jscomp$0 = 12 === request$jscomp$0.status ? request$jscomp$0.fatalError : x, replayNodes = task.replay.nodes, resumeSlots = task.replay.slots;
									request = logRecoverableError(request$jscomp$1, error$jscomp$0, errorInfo);
									abortRemainingReplayNodes(request$jscomp$1, boundary, replayNodes, resumeSlots, error$jscomp$0, request);
									request$jscomp$0.pendingRootTasks--;
									0 === request$jscomp$0.pendingRootTasks && completeShell(request$jscomp$0);
									request$jscomp$0.allPendingTasks--;
									0 === request$jscomp$0.allPendingTasks && completeAll(request$jscomp$0);
								}
							}
						}
					} else if (request$jscomp$0 = void 0, request$jscomp$1 = segment, 0 === request$jscomp$1.status) {
						request$jscomp$1.status = 6;
						switchContext(task.context);
						var childrenLength = request$jscomp$1.children.length, chunkLength = request$jscomp$1.chunks.length;
						try {
							retryNode(request, task), pushSegmentFinale(request$jscomp$1.chunks, request.renderState, request$jscomp$1.lastPushedText, request$jscomp$1.textEmbedded), task.abortSet.delete(task), request$jscomp$1.status = 1, finishedTask(request, task.blockedBoundary, task.row, request$jscomp$1);
						} catch (thrownValue) {
							resetHooksState();
							request$jscomp$1.children.length = childrenLength;
							request$jscomp$1.chunks.length = chunkLength;
							var x$jscomp$0 = thrownValue === SuspenseException ? getSuspendedThenable() : 12 === request.status ? request.fatalError : thrownValue;
							if (12 === request.status && null !== request.trackedPostpones) {
								var trackedPostpones = request.trackedPostpones, thrownInfo = getThrownInfo(task.componentStack);
								task.abortSet.delete(task);
								logRecoverableError(request, x$jscomp$0, thrownInfo);
								trackPostpone(request, trackedPostpones, task, request$jscomp$1);
								finishedTask(request, task.blockedBoundary, task.row, request$jscomp$1);
							} else if ("object" === typeof x$jscomp$0 && null !== x$jscomp$0 && "function" === typeof x$jscomp$0.then) {
								request$jscomp$1.status = 0;
								task.thenableState = thrownValue === SuspenseException ? getThenableStateAfterSuspending() : null;
								var ping$jscomp$0 = task.ping;
								x$jscomp$0.then(ping$jscomp$0, ping$jscomp$0);
							} else {
								var errorInfo$jscomp$0 = getThrownInfo(task.componentStack);
								task.abortSet.delete(task);
								request$jscomp$1.status = 4;
								var boundary$jscomp$0 = task.blockedBoundary, row = task.row;
								null !== row && 0 === --row.pendingTasks && finishSuspenseListRow(request, row);
								request.allPendingTasks--;
								request$jscomp$0 = logRecoverableError(request, x$jscomp$0, errorInfo$jscomp$0);
								if (null === boundary$jscomp$0) fatalError(request, x$jscomp$0);
								else if (boundary$jscomp$0.pendingTasks--, 4 !== boundary$jscomp$0.status) {
									boundary$jscomp$0.status = 4;
									boundary$jscomp$0.errorDigest = request$jscomp$0;
									untrackBoundary(request, boundary$jscomp$0);
									var boundaryRow = boundary$jscomp$0.row;
									null !== boundaryRow && 0 === --boundaryRow.pendingTasks && finishSuspenseListRow(request, boundaryRow);
									boundary$jscomp$0.parentFlushed && request.clientRenderedBoundaries.push(boundary$jscomp$0);
									0 === request.pendingRootTasks && null === request.trackedPostpones && null !== boundary$jscomp$0.contentPreamble && preparePreamble(request);
								}
								0 === request.allPendingTasks && completeAll(request);
							}
						}
					}
				}
				pingedTasks.splice(0, i);
				null !== request$jscomp$2.destination && flushCompletedQueues(request$jscomp$2, request$jscomp$2.destination);
			} catch (error) {
				logRecoverableError(request$jscomp$2, error, {}), fatalError(request$jscomp$2, error);
			} finally {
				currentResumableState = prevResumableState, ReactSharedInternals.H = prevDispatcher, ReactSharedInternals.A = prevAsyncDispatcher, prevDispatcher === HooksDispatcher && switchContext(prevContext), currentRequest = prevRequest;
			}
		}
	}
	function preparePreambleFromSubtree(request, segment, collectedPreambleSegments) {
		segment.preambleChildren.length && collectedPreambleSegments.push(segment.preambleChildren);
		for (var pendingPreambles = !1, i = 0; i < segment.children.length; i++) pendingPreambles = preparePreambleFromSegment(request, segment.children[i], collectedPreambleSegments) || pendingPreambles;
		return pendingPreambles;
	}
	function preparePreambleFromSegment(request, segment, collectedPreambleSegments) {
		var boundary = segment.boundary;
		if (null === boundary) return preparePreambleFromSubtree(request, segment, collectedPreambleSegments);
		var preamble = boundary.contentPreamble, fallbackPreamble = boundary.fallbackPreamble;
		if (null === preamble || null === fallbackPreamble) return !1;
		switch (boundary.status) {
			case 1:
				hoistPreambleState(request.renderState, preamble);
				request.byteSize += boundary.byteSize;
				segment = boundary.completedSegments[0];
				if (!segment) throw Error(formatProdErrorMessage(391));
				return preparePreambleFromSubtree(request, segment, collectedPreambleSegments);
			case 5: if (null !== request.trackedPostpones) return !0;
			case 4: if (1 === segment.status) return hoistPreambleState(request.renderState, fallbackPreamble), preparePreambleFromSubtree(request, segment, collectedPreambleSegments);
			default: return !0;
		}
	}
	function preparePreamble(request) {
		if (request.completedRootSegment && null === request.completedPreambleSegments) {
			var collectedPreambleSegments = [], originalRequestByteSize = request.byteSize, hasPendingPreambles = preparePreambleFromSegment(request, request.completedRootSegment, collectedPreambleSegments), preamble = request.renderState.preamble;
			!1 === hasPendingPreambles || preamble.headChunks && preamble.bodyChunks ? request.completedPreambleSegments = collectedPreambleSegments : request.byteSize = originalRequestByteSize;
		}
	}
	function flushSubtree(request, destination, segment, hoistableState) {
		segment.parentFlushed = !0;
		switch (segment.status) {
			case 0: segment.id = request.nextSegmentId++;
			case 5: return hoistableState = segment.id, segment.lastPushedText = !1, segment.textEmbedded = !1, request = request.renderState, destination.push("<template id=\""), destination.push(request.placeholderPrefix), request = hoistableState.toString(16), destination.push(request), destination.push("\"></template>");
			case 1:
				segment.status = 2;
				var r = !0, chunks = segment.chunks, chunkIdx = 0;
				segment = segment.children;
				for (var childIdx = 0; childIdx < segment.length; childIdx++) {
					for (r = segment[childIdx]; chunkIdx < r.index; chunkIdx++) destination.push(chunks[chunkIdx]);
					r = flushSegment(request, destination, r, hoistableState);
				}
				for (; chunkIdx < chunks.length - 1; chunkIdx++) destination.push(chunks[chunkIdx]);
				chunkIdx < chunks.length && (r = destination.push(chunks[chunkIdx]));
				return r;
			case 3: return !0;
			default: throw Error(formatProdErrorMessage(390));
		}
	}
	var flushedByteSize = 0;
	function flushSegment(request, destination, segment, hoistableState) {
		var boundary = segment.boundary;
		if (null === boundary) return flushSubtree(request, destination, segment, hoistableState);
		boundary.parentFlushed = !0;
		if (4 === boundary.status) {
			var row = boundary.row;
			null !== row && 0 === --row.pendingTasks && finishSuspenseListRow(request, row);
			request.renderState.generateStaticMarkup || (boundary = boundary.errorDigest, destination.push("<!--$!-->"), destination.push("<template"), boundary && (destination.push(" data-dgst=\""), boundary = escapeTextForBrowser(boundary), destination.push(boundary), destination.push("\"")), destination.push("></template>"));
			flushSubtree(request, destination, segment, hoistableState);
			request = request.renderState.generateStaticMarkup ? !0 : destination.push("<!--/$-->");
			return request;
		}
		if (1 !== boundary.status) return 0 === boundary.status && (boundary.rootSegmentID = request.nextSegmentId++), 0 < boundary.completedSegments.length && request.partialBoundaries.push(boundary), writeStartPendingSuspenseBoundary(destination, request.renderState, boundary.rootSegmentID), hoistableState && hoistHoistables(hoistableState, boundary.fallbackState), flushSubtree(request, destination, segment, hoistableState), destination.push("<!--/$-->");
		if (!flushingPartialBoundaries && isEligibleForOutlining(request, boundary) && flushedByteSize + boundary.byteSize > request.progressiveChunkSize) return boundary.rootSegmentID = request.nextSegmentId++, request.completedBoundaries.push(boundary), writeStartPendingSuspenseBoundary(destination, request.renderState, boundary.rootSegmentID), flushSubtree(request, destination, segment, hoistableState), destination.push("<!--/$-->");
		flushedByteSize += boundary.byteSize;
		hoistableState && hoistHoistables(hoistableState, boundary.contentState);
		segment = boundary.row;
		null !== segment && isEligibleForOutlining(request, boundary) && 0 === --segment.pendingTasks && finishSuspenseListRow(request, segment);
		request.renderState.generateStaticMarkup || destination.push("<!--$-->");
		segment = boundary.completedSegments;
		if (1 !== segment.length) throw Error(formatProdErrorMessage(391));
		flushSegment(request, destination, segment[0], hoistableState);
		request = request.renderState.generateStaticMarkup ? !0 : destination.push("<!--/$-->");
		return request;
	}
	function flushSegmentContainer(request, destination, segment, hoistableState) {
		writeStartSegment(destination, request.renderState, segment.parentFormatContext, segment.id);
		flushSegment(request, destination, segment, hoistableState);
		return writeEndSegment(destination, segment.parentFormatContext);
	}
	function flushCompletedBoundary(request, destination, boundary) {
		flushedByteSize = boundary.byteSize;
		for (var completedSegments = boundary.completedSegments, i = 0; i < completedSegments.length; i++) flushPartiallyCompletedSegment(request, destination, boundary, completedSegments[i]);
		completedSegments.length = 0;
		completedSegments = boundary.row;
		null !== completedSegments && isEligibleForOutlining(request, boundary) && 0 === --completedSegments.pendingTasks && finishSuspenseListRow(request, completedSegments);
		writeHoistablesForBoundary(destination, boundary.contentState, request.renderState);
		completedSegments = request.resumableState;
		request = request.renderState;
		i = boundary.rootSegmentID;
		boundary = boundary.contentState;
		var requiresStyleInsertion = request.stylesToHoist;
		request.stylesToHoist = !1;
		destination.push(request.startInlineScript);
		destination.push(">");
		requiresStyleInsertion ? (0 === (completedSegments.instructions & 4) && (completedSegments.instructions |= 4, destination.push("$RX=function(b,c,d,e,f){var a=document.getElementById(b);a&&(b=a.previousSibling,b.data=\"$!\",a=a.dataset,c&&(a.dgst=c),d&&(a.msg=d),e&&(a.stck=e),f&&(a.cstck=f),b._reactRetry&&b._reactRetry())};")), 0 === (completedSegments.instructions & 2) && (completedSegments.instructions |= 2, destination.push("$RB=[];$RV=function(a){$RT=performance.now();for(var b=0;b<a.length;b+=2){var c=a[b],e=a[b+1];null!==e.parentNode&&e.parentNode.removeChild(e);var f=c.parentNode;if(f){var g=c.previousSibling,h=0;do{if(c&&8===c.nodeType){var d=c.data;if(\"/$\"===d||\"/&\"===d)if(0===h)break;else h--;else\"$\"!==d&&\"$?\"!==d&&\"$~\"!==d&&\"$!\"!==d&&\"&\"!==d||h++}d=c.nextSibling;f.removeChild(c);c=d}while(c);for(;e.firstChild;)f.insertBefore(e.firstChild,c);g.data=\"$\";g._reactRetry&&requestAnimationFrame(g._reactRetry)}}a.length=0};\n$RC=function(a,b){if(b=document.getElementById(b))(a=document.getElementById(a))?(a.previousSibling.data=\"$~\",$RB.push(a,b),2===$RB.length&&(\"number\"!==typeof $RT?requestAnimationFrame($RV.bind(null,$RB)):(a=performance.now(),setTimeout($RV.bind(null,$RB),2300>a&&2E3<a?2300-a:$RT+300-a)))):b.parentNode.removeChild(b)};")), 0 === (completedSegments.instructions & 8) ? (completedSegments.instructions |= 8, destination.push("$RM=new Map;$RR=function(n,w,p){function u(q){this._p=null;q()}for(var r=new Map,t=document,h,b,e=t.querySelectorAll(\"link[data-precedence],style[data-precedence]\"),v=[],k=0;b=e[k++];)\"not all\"===b.getAttribute(\"media\")?v.push(b):(\"LINK\"===b.tagName&&$RM.set(b.getAttribute(\"href\"),b),r.set(b.dataset.precedence,h=b));e=0;b=[];var l,a;for(k=!0;;){if(k){var f=p[e++];if(!f){k=!1;e=0;continue}var c=!1,m=0;var d=f[m++];if(a=$RM.get(d)){var g=a._p;c=!0}else{a=t.createElement(\"link\");a.href=d;a.rel=\n\"stylesheet\";for(a.dataset.precedence=l=f[m++];g=f[m++];)a.setAttribute(g,f[m++]);g=a._p=new Promise(function(q,x){a.onload=u.bind(a,q);a.onerror=u.bind(a,x)});$RM.set(d,a)}d=a.getAttribute(\"media\");!g||d&&!matchMedia(d).matches||b.push(g);if(c)continue}else{a=v[e++];if(!a)break;l=a.getAttribute(\"data-precedence\");a.removeAttribute(\"media\")}c=r.get(l)||h;c===h&&(h=a);r.set(l,a);c?c.parentNode.insertBefore(a,c.nextSibling):(c=t.head,c.insertBefore(a,c.firstChild))}if(p=document.getElementById(n))p.previousSibling.data=\n\"$~\";Promise.all(b).then($RC.bind(null,n,w),$RX.bind(null,n,\"CSS failed to load\"))};$RR(\"")) : destination.push("$RR(\"")) : (0 === (completedSegments.instructions & 2) && (completedSegments.instructions |= 2, destination.push("$RB=[];$RV=function(a){$RT=performance.now();for(var b=0;b<a.length;b+=2){var c=a[b],e=a[b+1];null!==e.parentNode&&e.parentNode.removeChild(e);var f=c.parentNode;if(f){var g=c.previousSibling,h=0;do{if(c&&8===c.nodeType){var d=c.data;if(\"/$\"===d||\"/&\"===d)if(0===h)break;else h--;else\"$\"!==d&&\"$?\"!==d&&\"$~\"!==d&&\"$!\"!==d&&\"&\"!==d||h++}d=c.nextSibling;f.removeChild(c);c=d}while(c);for(;e.firstChild;)f.insertBefore(e.firstChild,c);g.data=\"$\";g._reactRetry&&requestAnimationFrame(g._reactRetry)}}a.length=0};\n$RC=function(a,b){if(b=document.getElementById(b))(a=document.getElementById(a))?(a.previousSibling.data=\"$~\",$RB.push(a,b),2===$RB.length&&(\"number\"!==typeof $RT?requestAnimationFrame($RV.bind(null,$RB)):(a=performance.now(),setTimeout($RV.bind(null,$RB),2300>a&&2E3<a?2300-a:$RT+300-a)))):b.parentNode.removeChild(b)};")), destination.push("$RC(\""));
		completedSegments = i.toString(16);
		destination.push(request.boundaryPrefix);
		destination.push(completedSegments);
		destination.push("\",\"");
		destination.push(request.segmentPrefix);
		destination.push(completedSegments);
		requiresStyleInsertion ? (destination.push("\","), writeStyleResourceDependenciesInJS(destination, boundary)) : destination.push("\"");
		boundary = destination.push(")<\/script>");
		return writeBootstrap(destination, request) && boundary;
	}
	function flushPartiallyCompletedSegment(request, destination, boundary, segment) {
		if (2 === segment.status) return !0;
		var hoistableState = boundary.contentState, segmentID = segment.id;
		if (-1 === segmentID) {
			if (-1 === (segment.id = boundary.rootSegmentID)) throw Error(formatProdErrorMessage(392));
			return flushSegmentContainer(request, destination, segment, hoistableState);
		}
		if (segmentID === boundary.rootSegmentID) return flushSegmentContainer(request, destination, segment, hoistableState);
		flushSegmentContainer(request, destination, segment, hoistableState);
		boundary = request.resumableState;
		request = request.renderState;
		destination.push(request.startInlineScript);
		destination.push(">");
		0 === (boundary.instructions & 1) ? (boundary.instructions |= 1, destination.push("$RS=function(a,b){a=document.getElementById(a);b=document.getElementById(b);for(a.parentNode.removeChild(a);a.firstChild;)b.parentNode.insertBefore(a.firstChild,b);b.parentNode.removeChild(b)};$RS(\"")) : destination.push("$RS(\"");
		destination.push(request.segmentPrefix);
		segmentID = segmentID.toString(16);
		destination.push(segmentID);
		destination.push("\",\"");
		destination.push(request.placeholderPrefix);
		destination.push(segmentID);
		destination = destination.push("\")<\/script>");
		return destination;
	}
	var flushingPartialBoundaries = !1;
	function flushCompletedQueues(request, destination) {
		try {
			if (!(0 < request.pendingRootTasks)) {
				var i, completedRootSegment = request.completedRootSegment;
				if (null !== completedRootSegment) {
					if (5 === completedRootSegment.status) return;
					var completedPreambleSegments = request.completedPreambleSegments;
					if (null === completedPreambleSegments) return;
					flushedByteSize = request.byteSize;
					var resumableState = request.resumableState, renderState = request.renderState, preamble = renderState.preamble, htmlChunks = preamble.htmlChunks, headChunks = preamble.headChunks, i$jscomp$0;
					if (htmlChunks) {
						for (i$jscomp$0 = 0; i$jscomp$0 < htmlChunks.length; i$jscomp$0++) destination.push(htmlChunks[i$jscomp$0]);
						if (headChunks) for (i$jscomp$0 = 0; i$jscomp$0 < headChunks.length; i$jscomp$0++) destination.push(headChunks[i$jscomp$0]);
						else {
							var chunk = startChunkForTag("head");
							destination.push(chunk);
							destination.push(">");
						}
					} else if (headChunks) for (i$jscomp$0 = 0; i$jscomp$0 < headChunks.length; i$jscomp$0++) destination.push(headChunks[i$jscomp$0]);
					var charsetChunks = renderState.charsetChunks;
					for (i$jscomp$0 = 0; i$jscomp$0 < charsetChunks.length; i$jscomp$0++) destination.push(charsetChunks[i$jscomp$0]);
					charsetChunks.length = 0;
					renderState.preconnects.forEach(flushResource, destination);
					renderState.preconnects.clear();
					var viewportChunks = renderState.viewportChunks;
					for (i$jscomp$0 = 0; i$jscomp$0 < viewportChunks.length; i$jscomp$0++) destination.push(viewportChunks[i$jscomp$0]);
					viewportChunks.length = 0;
					renderState.fontPreloads.forEach(flushResource, destination);
					renderState.fontPreloads.clear();
					renderState.highImagePreloads.forEach(flushResource, destination);
					renderState.highImagePreloads.clear();
					currentlyFlushingRenderState = renderState;
					renderState.styles.forEach(flushStylesInPreamble, destination);
					currentlyFlushingRenderState = null;
					var importMapChunks = renderState.importMapChunks;
					for (i$jscomp$0 = 0; i$jscomp$0 < importMapChunks.length; i$jscomp$0++) destination.push(importMapChunks[i$jscomp$0]);
					importMapChunks.length = 0;
					renderState.bootstrapScripts.forEach(flushResource, destination);
					renderState.scripts.forEach(flushResource, destination);
					renderState.scripts.clear();
					renderState.bulkPreloads.forEach(flushResource, destination);
					renderState.bulkPreloads.clear();
					resumableState.instructions |= 32;
					var hoistableChunks = renderState.hoistableChunks;
					for (i$jscomp$0 = 0; i$jscomp$0 < hoistableChunks.length; i$jscomp$0++) destination.push(hoistableChunks[i$jscomp$0]);
					for (resumableState = hoistableChunks.length = 0; resumableState < completedPreambleSegments.length; resumableState++) {
						var segments = completedPreambleSegments[resumableState];
						for (renderState = 0; renderState < segments.length; renderState++) flushSegment(request, destination, segments[renderState], null);
					}
					var preamble$jscomp$0 = request.renderState.preamble, headChunks$jscomp$0 = preamble$jscomp$0.headChunks;
					if (preamble$jscomp$0.htmlChunks || headChunks$jscomp$0) {
						var chunk$jscomp$0 = endChunkForTag("head");
						destination.push(chunk$jscomp$0);
					}
					var bodyChunks = preamble$jscomp$0.bodyChunks;
					if (bodyChunks) for (completedPreambleSegments = 0; completedPreambleSegments < bodyChunks.length; completedPreambleSegments++) destination.push(bodyChunks[completedPreambleSegments]);
					flushSegment(request, destination, completedRootSegment, null);
					request.completedRootSegment = null;
					var renderState$jscomp$0 = request.renderState;
					if (0 !== request.allPendingTasks || 0 !== request.clientRenderedBoundaries.length || 0 !== request.completedBoundaries.length || null !== request.trackedPostpones && (0 !== request.trackedPostpones.rootNodes.length || null !== request.trackedPostpones.rootSlots)) {
						var resumableState$jscomp$0 = request.resumableState;
						if (0 === (resumableState$jscomp$0.instructions & 64)) {
							resumableState$jscomp$0.instructions |= 64;
							destination.push(renderState$jscomp$0.startInlineScript);
							if (0 === (resumableState$jscomp$0.instructions & 32)) {
								resumableState$jscomp$0.instructions |= 32;
								var shellId = "_" + resumableState$jscomp$0.idPrefix + "R_";
								destination.push(" id=\"");
								var chunk$jscomp$1 = escapeTextForBrowser(shellId);
								destination.push(chunk$jscomp$1);
								destination.push("\"");
							}
							destination.push(">");
							destination.push("requestAnimationFrame(function(){$RT=performance.now()});");
							destination.push("<\/script>");
						}
					}
					writeBootstrap(destination, renderState$jscomp$0);
				}
				var renderState$jscomp$1 = request.renderState;
				completedRootSegment = 0;
				var viewportChunks$jscomp$0 = renderState$jscomp$1.viewportChunks;
				for (completedRootSegment = 0; completedRootSegment < viewportChunks$jscomp$0.length; completedRootSegment++) destination.push(viewportChunks$jscomp$0[completedRootSegment]);
				viewportChunks$jscomp$0.length = 0;
				renderState$jscomp$1.preconnects.forEach(flushResource, destination);
				renderState$jscomp$1.preconnects.clear();
				renderState$jscomp$1.fontPreloads.forEach(flushResource, destination);
				renderState$jscomp$1.fontPreloads.clear();
				renderState$jscomp$1.highImagePreloads.forEach(flushResource, destination);
				renderState$jscomp$1.highImagePreloads.clear();
				renderState$jscomp$1.styles.forEach(preloadLateStyles, destination);
				renderState$jscomp$1.scripts.forEach(flushResource, destination);
				renderState$jscomp$1.scripts.clear();
				renderState$jscomp$1.bulkPreloads.forEach(flushResource, destination);
				renderState$jscomp$1.bulkPreloads.clear();
				var hoistableChunks$jscomp$0 = renderState$jscomp$1.hoistableChunks;
				for (completedRootSegment = 0; completedRootSegment < hoistableChunks$jscomp$0.length; completedRootSegment++) destination.push(hoistableChunks$jscomp$0[completedRootSegment]);
				hoistableChunks$jscomp$0.length = 0;
				var clientRenderedBoundaries = request.clientRenderedBoundaries;
				for (i = 0; i < clientRenderedBoundaries.length; i++) {
					var boundary = clientRenderedBoundaries[i];
					renderState$jscomp$1 = destination;
					var resumableState$jscomp$1 = request.resumableState, renderState$jscomp$2 = request.renderState, id = boundary.rootSegmentID, errorDigest = boundary.errorDigest;
					renderState$jscomp$1.push(renderState$jscomp$2.startInlineScript);
					renderState$jscomp$1.push(">");
					0 === (resumableState$jscomp$1.instructions & 4) ? (resumableState$jscomp$1.instructions |= 4, renderState$jscomp$1.push("$RX=function(b,c,d,e,f){var a=document.getElementById(b);a&&(b=a.previousSibling,b.data=\"$!\",a=a.dataset,c&&(a.dgst=c),d&&(a.msg=d),e&&(a.stck=e),f&&(a.cstck=f),b._reactRetry&&b._reactRetry())};;$RX(\"")) : renderState$jscomp$1.push("$RX(\"");
					renderState$jscomp$1.push(renderState$jscomp$2.boundaryPrefix);
					var chunk$jscomp$2 = id.toString(16);
					renderState$jscomp$1.push(chunk$jscomp$2);
					renderState$jscomp$1.push("\"");
					if (errorDigest) {
						renderState$jscomp$1.push(",");
						var chunk$jscomp$3 = escapeJSStringsForInstructionScripts(errorDigest || "");
						renderState$jscomp$1.push(chunk$jscomp$3);
					}
					var JSCompiler_inline_result = renderState$jscomp$1.push(")<\/script>");
					if (!JSCompiler_inline_result) {
						request.destination = null;
						i++;
						clientRenderedBoundaries.splice(0, i);
						return;
					}
				}
				clientRenderedBoundaries.splice(0, i);
				var completedBoundaries = request.completedBoundaries;
				for (i = 0; i < completedBoundaries.length; i++) if (!flushCompletedBoundary(request, destination, completedBoundaries[i])) {
					request.destination = null;
					i++;
					completedBoundaries.splice(0, i);
					return;
				}
				completedBoundaries.splice(0, i);
				flushingPartialBoundaries = !0;
				var partialBoundaries = request.partialBoundaries;
				for (i = 0; i < partialBoundaries.length; i++) {
					var boundary$69 = partialBoundaries[i];
					a: {
						clientRenderedBoundaries = request;
						boundary = destination;
						flushedByteSize = boundary$69.byteSize;
						var completedSegments = boundary$69.completedSegments;
						for (JSCompiler_inline_result = 0; JSCompiler_inline_result < completedSegments.length; JSCompiler_inline_result++) if (!flushPartiallyCompletedSegment(clientRenderedBoundaries, boundary, boundary$69, completedSegments[JSCompiler_inline_result])) {
							JSCompiler_inline_result++;
							completedSegments.splice(0, JSCompiler_inline_result);
							var JSCompiler_inline_result$jscomp$0 = !1;
							break a;
						}
						completedSegments.splice(0, JSCompiler_inline_result);
						var row = boundary$69.row;
						null !== row && row.together && 1 === boundary$69.pendingTasks && (1 === row.pendingTasks ? unblockSuspenseListRow(clientRenderedBoundaries, row, row.hoistables) : row.pendingTasks--);
						JSCompiler_inline_result$jscomp$0 = writeHoistablesForBoundary(boundary, boundary$69.contentState, clientRenderedBoundaries.renderState);
					}
					if (!JSCompiler_inline_result$jscomp$0) {
						request.destination = null;
						i++;
						partialBoundaries.splice(0, i);
						return;
					}
				}
				partialBoundaries.splice(0, i);
				flushingPartialBoundaries = !1;
				var largeBoundaries = request.completedBoundaries;
				for (i = 0; i < largeBoundaries.length; i++) if (!flushCompletedBoundary(request, destination, largeBoundaries[i])) {
					request.destination = null;
					i++;
					largeBoundaries.splice(0, i);
					return;
				}
				largeBoundaries.splice(0, i);
			}
		} finally {
			flushingPartialBoundaries = !1, 0 === request.allPendingTasks && 0 === request.clientRenderedBoundaries.length && 0 === request.completedBoundaries.length && (request.flushScheduled = !1, i = request.resumableState, i.hasBody && (partialBoundaries = endChunkForTag("body"), destination.push(partialBoundaries)), i.hasHtml && (i = endChunkForTag("html"), destination.push(i)), request.status = 14, destination.push(null), request.destination = null);
		}
	}
	function enqueueFlush(request) {
		if (!1 === request.flushScheduled && 0 === request.pingedTasks.length && null !== request.destination) {
			request.flushScheduled = !0;
			var destination = request.destination;
			destination ? flushCompletedQueues(request, destination) : request.flushScheduled = !1;
		}
	}
	function startFlowing(request, destination) {
		if (13 === request.status) request.status = 14, destination.destroy(request.fatalError);
		else if (14 !== request.status && null === request.destination) {
			request.destination = destination;
			try {
				flushCompletedQueues(request, destination);
			} catch (error) {
				logRecoverableError(request, error, {}), fatalError(request, error);
			}
		}
	}
	function abort(request, reason) {
		if (11 === request.status || 10 === request.status) request.status = 12;
		try {
			var abortableTasks = request.abortableTasks;
			if (0 < abortableTasks.size) {
				var error = void 0 === reason ? Error(formatProdErrorMessage(432)) : "object" === typeof reason && null !== reason && "function" === typeof reason.then ? Error(formatProdErrorMessage(530)) : reason;
				request.fatalError = error;
				abortableTasks.forEach(function(task) {
					return abortTask(task, request, error);
				});
				abortableTasks.clear();
			}
			null !== request.destination && flushCompletedQueues(request, request.destination);
		} catch (error$71) {
			logRecoverableError(request, error$71, {}), fatalError(request, error$71);
		}
	}
	function addToReplayParent(node, parentKeyPath, trackedPostpones) {
		if (null === parentKeyPath) trackedPostpones.rootNodes.push(node);
		else {
			var workingMap = trackedPostpones.workingMap, parentNode = workingMap.get(parentKeyPath);
			void 0 === parentNode && (parentNode = [
				parentKeyPath[1],
				parentKeyPath[2],
				[],
				null
			], workingMap.set(parentKeyPath, parentNode), addToReplayParent(parentNode, parentKeyPath[0], trackedPostpones));
			parentNode[2].push(node);
		}
	}
	function onError() {}
	function renderToStringImpl(children, options, generateStaticMarkup, abortReason) {
		var didFatal = !1, fatalError = null, result = "", readyToStream = !1;
		options = createResumableState(options ? options.identifierPrefix : void 0);
		children = createRequest(children, options, createRenderState(options, generateStaticMarkup), createFormatContext(0, null, 0, null), Infinity, onError, void 0, function() {
			readyToStream = !0;
		}, void 0, void 0, void 0);
		children.flushScheduled = null !== children.destination;
		performWork(children);
		10 === children.status && (children.status = 11);
		null === children.trackedPostpones && safelyEmitEarlyPreloads(children, 0 === children.pendingRootTasks);
		abort(children, abortReason);
		startFlowing(children, {
			push: function(chunk) {
				null !== chunk && (result += chunk);
				return !0;
			},
			destroy: function(error) {
				didFatal = !0;
				fatalError = error;
			}
		});
		if (didFatal && fatalError !== abortReason) throw fatalError;
		if (!readyToStream) throw Error(formatProdErrorMessage(426));
		return result;
	}
	exports.renderToStaticMarkup = function(children, options) {
		return renderToStringImpl(children, options, !0, "The server used \"renderToStaticMarkup\" which does not support Suspense. If you intended to have the server wait for the suspended component please switch to \"renderToReadableStream\" which supports Suspense on the server");
	};
	exports.renderToString = function(children, options) {
		return renderToStringImpl(children, options, !1, "The server used \"renderToString\" which does not support Suspense. If you intended for this Suspense boundary to render the fallback content on the server consider throwing an Error somewhere within the Suspense boundary. If you intended to have the server wait for the suspended component please switch to \"renderToReadableStream\" which supports Suspense on the server");
	};
	exports.version = "19.2.7";
}));
//#endregion
//#region ../../node_modules/.pnpm/react-dom@19.2.7_react@19.2.7/node_modules/react-dom/server.edge.js
var require_server_edge = /* @__PURE__ */ __commonJSMin(((exports) => {
	var b;
	var l;
	b = require_react_dom_server_edge_production();
	l = require_react_dom_server_legacy_browser_production();
	exports.version = b.version;
	exports.renderToReadableStream = b.renderToReadableStream;
	exports.renderToString = l.renderToString;
	exports.renderToStaticMarkup = l.renderToStaticMarkup;
	exports.resume = b.resume;
}));
//#endregion
//#region ../../node_modules/.pnpm/react@19.2.7/node_modules/react/cjs/react-jsx-runtime.production.js
/**
* @license React
* react-jsx-runtime.production.js
*
* Copyright (c) Meta Platforms, Inc. and affiliates.
*
* This source code is licensed under the MIT license found in the
* LICENSE file in the root directory of this source tree.
*/
var require_react_jsx_runtime_production = /* @__PURE__ */ __commonJSMin(((exports) => {
	var REACT_ELEMENT_TYPE = Symbol.for("react.transitional.element");
	function jsxProd(type, config, maybeKey) {
		var key = null;
		void 0 !== maybeKey && (key = "" + maybeKey);
		void 0 !== config.key && (key = "" + config.key);
		if ("key" in config) {
			maybeKey = {};
			for (var propName in config) "key" !== propName && (maybeKey[propName] = config[propName]);
		} else maybeKey = config;
		config = maybeKey.ref;
		return {
			$$typeof: REACT_ELEMENT_TYPE,
			type,
			key,
			ref: void 0 !== config ? config : null,
			props: maybeKey
		};
	}
	exports.jsx = jsxProd;
	exports.jsxs = jsxProd;
}));
//#endregion
//#region ../../node_modules/.pnpm/react@19.2.7/node_modules/react/jsx-runtime.js
var require_jsx_runtime = /* @__PURE__ */ __commonJSMin(((exports, module) => {
	module.exports = require_react_jsx_runtime_production();
}));
//#endregion
//#region ../../node_modules/.pnpm/@react-router+dev@8.3.0_react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7__e6eace2dfe39ab91c9ffd38fa9744cb7/node_modules/@react-router/dev/dist/config/defaults/entry.server.web.tsx
var entry_server_web_exports = /* @__PURE__ */ __exportAll({
	default: () => handleRequest,
	streamTimeout: () => streamTimeout
});
var import_server_edge = require_server_edge();
var import_jsx_runtime = require_jsx_runtime();
var streamTimeout = 5e3;
async function handleRequest(request, responseStatusCode, responseHeaders, routerContext, _loadContext) {
	if (request.method.toUpperCase() === "HEAD") return new Response(null, {
		status: responseStatusCode,
		headers: responseHeaders
	});
	let shellRendered = false;
	let userAgent = request.headers.get("user-agent");
	const body = await (0, import_server_edge.renderToReadableStream)(/* @__PURE__ */ (0, import_jsx_runtime.jsx)(ServerRouter, {
		context: routerContext,
		url: request.url
	}), {
		signal: AbortSignal.timeout(6e3),
		onError(error) {
			responseStatusCode = 500;
			if (shellRendered) console.error(error);
		}
	});
	shellRendered = true;
	if (userAgent && isbot(userAgent) || routerContext.isSpaMode) await body.allReady;
	responseHeaders.set("Content-Type", "text/html");
	return new Response(body, {
		headers: responseHeaders,
		status: responseStatusCode
	});
}
//#endregion
//#region ../../packages/cache/src/telemetry.ts
/**
* The telemetry event-name CATALOG — the single, kernel-hosted registry of every
* event `type` this library emits, the same shape as the exported error classes. Each member is an
* event NAME; the value equals the key so a producer can reference a name as `TELEMETRY_EVENTS.<name>`
* (a literal type, refactor-safe) while `TelemetryEventName` (below) is the union of them all.
*
* The names are grouped by their emitting LAYER (kernel / L1 driver / react-router adapter /
* cloudflare adapter) — the annotation the charter requires. The machine-checked layer map (which
* fails to compile if a member is unannotated) lives in the catalog's exhaustiveness tooth in the
* test lane.
*
* GROWTH: adding a name is ADDITIVE and consumer-SAFE (a sink RECEIVES events, so a producer that
* emits a wider vocabulary hands a consumer a value from a set it already types against — narrowing,
* not widening, at the receiver) — so a new name is a MINOR-version change. Removing or renaming one
* is breaking.
*/
var TELEMETRY_EVENTS = {
	writeback_throttled: "writeback_throttled",
	l2_write_error: "l2_write_error",
	singleflight_option_mismatch: "singleflight_option_mismatch",
	store_read_suppressed: "store_read_suppressed",
	neg_suppressed: "neg_suppressed",
	private_evicted: "private_evicted",
	poll_tick_failed: "poll_tick_failed",
	l3_ineligible: "l3_ineligible",
	regid_divergence: "regid_divergence",
	bus_dial_failed: "bus_dial_failed",
	strong_degraded: "strong_degraded",
	registry_degraded: "registry_degraded",
	state_stale: "state_stale"
};
/** Report a swallowed sink failure to the injected hook, itself guarded. The hook is app code at the
* very end of the swallow chain, so its own failure has no meta-channel to report to — swallowed, so
* `safeEmit` cannot throw or float through it. */
function reportSinkError(hook, error) {
	if (hook === void 0) return;
	const called = hook;
	try {
		Promise.resolve(called(error)).catch(() => {});
	} catch {}
}
/**
* Emit ONE event through an app-supplied sink so that NOTHING it does can escape (
* the swallow-wrap extracted from the poll driver and made shared). A sink is arbitrary app code;
* both of its failure modes are neutralized here:
*
*   • a SYNCHRONOUS throw → the `try`/`catch` (without it the caller's own path would throw);
*   • a REJECTING thenable → the returned value is ADOPTED (`Promise.resolve(...).catch(...)`) so a
*     rejecting `async` sink can never float out as an unhandled rejection.
*
* On either failure the injected `onSinkError` hook is called exactly once for that emit. `safeEmit`
* NEVER throws and returns nothing the caller must await (adopt-don't-await: it must not inherit
* app-code latency on a background or response path).
*
* The TYPE is the structural tooth: `event` is a `TelemetryEvent`, whose `type` is the CATALOG
* union — so an off-catalog event name is a COMPILE error at the call site, never a grep finding.
*/
function safeEmit(sink, event, onSinkError) {
	if (sink === void 0) return;
	const called = sink;
	try {
		Promise.resolve(called(event)).catch((error) => {
			reportSinkError(onSinkError, error);
		});
	} catch (error) {
		reportSinkError(onSinkError, error);
	}
}
//#endregion
//#region ../../packages/cache/src/drivers.ts
/**
* Structural type-guard for `StoreWriteError`. Purely shape-based — it NEVER
* uses `instanceof`, so a harness fake and a real driver are recognized by the SAME
* check without a shared class. Total and safe on arbitrary `unknown`: rejects
* non-objects/null, requires `code` to be exactly one of the three literals and
* `retryable` to be a boolean.
*/
function isStoreWriteError(e) {
	if (typeof e !== "object" || e === null) return false;
	const code = "code" in e ? e.code : void 0;
	const retryable = "retryable" in e ? e.retryable : void 0;
	return (code === "throttled" || code === "too_large" || code === "unavailable") && typeof retryable === "boolean";
}
/**
* Structural type-guard for `StoreReadError` — shape-based, never `instanceof`,
* exactly as `isStoreWriteError` is. Total and safe on arbitrary `unknown`. A driver error
* that carries a read-suppressible `code` satisfies this whatever class raised it, which is
* the point: the own `unavailable` shape is recognized without a shared identity.
*/
function isStoreReadError(e) {
	if (typeof e !== "object" || e === null) return false;
	const code = "code" in e ? e.code : void 0;
	const retryable = "retryable" in e ? e.retryable : void 0;
	return (code === "throttled" || code === "unavailable") && typeof retryable === "boolean";
}
//#endregion
//#region ../../packages/cache/src/errors.ts
/**
* Thrown by every public entry point during the correctness-kernel phase. A RED
* invariant that fails with `NotImplementedError` is red for the right reason
* (the kernel is unimplemented), as opposed to a toolchain/config failure.
*/
var NotImplementedError = class extends Error {
	constructor(symbol) {
		super(`${symbol}: not implemented in the correctness-kernel phase`);
		this.name = "NotImplementedError";
	}
};
/**
* The single kernel-phase stub body. Every public function/method delegates here
* so the switch to real logic is a body replacement with zero signature churn.
* Callers pass their arguments through so parameters are genuinely referenced;
* inside an `async` method the thrown error becomes a rejected promise.
*/
function notImplemented(symbol, ..._args) {
	throw new NotImplementedError(symbol);
}
/**
* Distinguishes cache-originated timeouts (retriable — evidence about origin
* health) from caller aborts (not evidence of origin health).
*/
var CacheTimeoutError = class extends Error {
	source;
	constructor(source, message) {
		super(message ?? `cache operation timed out (source: ${source})`);
		this.name = "CacheTimeoutError";
		this.source = source;
	}
};
/**
* Thrown by a `strong` read when the registry is unreachable. Opt out with
* `onUnavailable: 'eventual'`.
*/
var RegistryUnavailableError = class extends Error {
	/**
	* `options.cause` carries the error this one displaced, where there was one. The serve-time
	* re-validation of a graced candidate runs INSIDE a failed fill: if the registry is unreachable
	* there, this error is what the caller sees instead of the factory failure that started it, so
	* the factory failure rides along rather than being dropped. Nothing is lost and callers get one
	* uniform type. Widened additively — `new RegistryUnavailableError(msg)` is unchanged.
	*/
	constructor(message, options) {
		super(message ?? "registry unreachable", options);
		this.name = "RegistryUnavailableError";
	}
};
/**
* Thrown by a read whose tag knowledge is UNKNOWN under `unknownPolicy: 'error'`.
*
* WHAT MAKES THIS DISTINCT FROM A MISS, AND WHY THE DISTINCTION IS THE POINT. Every other
* fail-closed resolution refills from origin, which is hard-safe exactly when the origin is truth.
* `'error'` is the policy for the case where it is NOT: a namespace under a takedown or a legal
* embargo. There, an origin that has not caught up yet can lose a race with the purge and hand back
* exactly the content that was supposed to vanish. So this policy declines to guess: it neither
* serves the unknown value nor replaces it
* with a fresh one, and the caller — who knows what the content is — decides. A policy that resolved
* to the same refill as `'miss'` would be a switch that reads as protection and does nothing, which
* is worse than no switch at all.
*
* Carries the key and the tags whose knowledge was unknown, so a caller can tell an embargoed
* dependency from a cold one. EXACTLY those: the tags this read could not establish, and no others.
* A payload of everything the entry declared cannot answer the question the field exists for — the
* caller would have to guess which of them was the problem, which is the guess this policy refuses
* to make on their behalf. Reserved (`__`-prefixed) tags never appear here: they are the kernel's
* own bookkeeping, they are unknown for exactly the same reason the user's are, and naming them
* tells the caller nothing they can act on.
*/
var UnknownTagError = class extends Error {
	key;
	tags;
	/**
	* `armedBy` names what selected this resolution — an explicit `unknownPolicy: 'error'`, or the
	* `takedownSensitive` override that outranks it. The message used to state the former
	* unconditionally, which was wrong precisely on the takedown path: the one place where the
	* operator most needs to know which switch fired is the one where the message named the other.
	*/
	constructor(key, tags, armedBy = "unknownPolicy") {
		const cause = armedBy === "takedownSensitive" ? "takedownSensitive is set" : "unknownPolicy is 'error'";
		super(`read for key ${JSON.stringify(key)} has unknown tag knowledge and ${cause} (§5a, E9)`);
		this.name = "UnknownTagError";
		this.key = key;
		this.tags = tags;
	}
};
/** A typed HTTP error carrying the offending status and response. */
var HttpError = class extends Error {
	status;
	response;
	constructor(response) {
		super(`HTTP ${response.status}`);
		this.name = "HttpError";
		this.status = response.status;
		this.response = response;
	}
};
/**
* Thrown at the `getOrSet`/`getOrSetEntry` write boundary when a caller-supplied
* tag is malformed — in particular a user tag beginning with the reserved `__`
* prefix. A real runtime error, never `NotImplementedError`: tag
* validation is not deferred kernel logic.
*/
var InvalidTagError = class extends Error {
	tag;
	constructor(tag, reason) {
		super(`invalid tag ${JSON.stringify(tag)}: ${reason}`);
		this.name = "InvalidTagError";
		this.tag = tag;
	}
};
/**
* Thrown by the plain `getOrSet` form when a fill is fenced past its write-back
* retry attempt budget: a hard delete keeps landing mid-fill/mid-
* refresh so the accepted result never postdates every purge visible at
* write-back, and the fill fails within its attempt budget rather than ever
* storing or serving a fenced value. A real runtime outcome, never
* `NotImplementedError` — the entry form (`getOrSetEntry`) surfaces the same
* outcome as `{ value: undefined }` instead of throwing.
*/
var FencedError = class extends Error {
	key;
	constructor(key, message) {
		super(message ?? `fill for key ${JSON.stringify(key)} fenced past its write-back attempt budget (§5.0/MI6)`);
		this.name = "FencedError";
		this.key = key;
	}
};
/**
* Thrown when a factory declares a dependency after it has settled: a
* `ctx.dependsOn`/`ctx.setTags` call reaching the context AFTER the factory promise
* resolved or rejected. The fill's tag set closes at settle, so a later declaration is
* outside the value's lifetime and cannot join it. A real runtime error, never
* `NotImplementedError`. Carries the offending key.
*/
var FactorySettledError = class extends Error {
	key;
	constructor(key) {
		super(`factory for key ${JSON.stringify(key)} declared a dependency after it settled (§2.1, E24)`);
		this.name = "FactorySettledError";
		this.key = key;
	}
};
/**
* The `EntryFactoryCtx.skip()` sentinel (M10, invariant 24) — a private
* throw/catch signal, analogous to how a classified factory error rides
* `fail()`. `skip()` is typed `never` so it never falsely resolves `undefined`
* as a caller's `T`; `fillAndStore` catches this exact class and turns it into
* a `{ skipped: true, value: undefined }` outcome — nothing is stored, nothing
* is served. Never part of the public surface (not exported from `index.ts`).
*/
var SkipSignalError = class extends Error {
	constructor() {
		super("ctx.skip() called — the fill is skipped (nothing stored, nothing served)");
		this.name = "SkipSignalError";
	}
};
//#endregion
//#region ../../packages/cache/src/duration.ts
/**
* Milliseconds per unit — the ONE `Duration`→ms grammar source. Shared with
* `cache.ts`'s coarse `durationMs` parser (which imports this const) so the overflow
* check here and the retention/heartbeat parse there agree on the factor table byte-for-byte.
* `m` = minutes.
*/
var DURATION_UNIT_MS = {
	d: 864e5,
	h: 36e5,
	m: 6e4,
	ms: 1,
	s: 1e3
};
//#endregion
//#region ../../packages/cache/src/http.ts
/** The default retriable HTTP status set (Cloudflare 52x joins it). */
var RETRIABLE_STATUSES = /* @__PURE__ */ new Set([
	408,
	425,
	429,
	500,
	502,
	503,
	504,
	520,
	521,
	522,
	523,
	524,
	525,
	526,
	527,
	530
]);
/**
* The default `staleIfError` predicate — retriable: the default
* retriable set, network errors, and cache-originated timeouts. `404/410/403` are
* facts, never masked (never retriable) — a caller-abort `CacheTimeoutError` with
* `source:'caller'` is likewise not evidence of origin health.
*/
function isRetriableHttp(err) {
	if (err instanceof HttpError) return RETRIABLE_STATUSES.has(err.status);
	if (err instanceof CacheTimeoutError) return err.source === "cache";
	return err instanceof TypeError;
}
//#endregion
//#region ../../packages/cache/src/registry/mirror-keys.ts
/**
* Build the registry-scoped mirror keys for `regId`. `regId` is expected to be
* already validated (it enters the machine only via `configure`, which calls `assertRegId`);
* this is a pure formatter on the hot flush/resync path, so it does not re-validate. Both the
* writer (execute.ts) and the reader (invalidation.ts) address keys ONLY through here.
*/
function mirrorKeys(regId) {
	const prefix = `__reg:${regId}:`;
	return {
		deltaKey: (seq) => `${prefix}delta:${seq}`,
		snapKey: (seq) => `${prefix}snap:${seq}`,
		stateKey: `${prefix}state`
	};
}
//#endregion
//#region ../../packages/cache/src/tags.ts
/** The two reserved tag namespaces: the per-key digest and the
* per-namespace watermark. A user tag beginning with this prefix is rejected
* at write — only the kernel itself may stamp `__k:<h>` / `__ns:<name>`. */
var RESERVED_TAG_PREFIX = "__";
/** Whether `tag` falls in the reserved `__`-prefixed namespace. */
function isReservedTag(tag) {
	return tag.startsWith(RESERVED_TAG_PREFIX);
}
/**
* Validate one caller-supplied tag at the write boundary. Throws a
* real `InvalidTagError` — never `NotImplementedError` — the instant a
* `__`-prefixed tag reaches `getOrSet`/`getOrSetEntry`/a purge selector, so a
* forged reserved tag can never be smuggled in as a "user" tag.
*
* Division of labor: the CHARSET/length/non-empty authority is the builders — `t`
* validates-and-rejects, `compound` escapes — so a `Tag` they produce is already
* well-formed. This write-time check is deliberately the NARROW `__`-prefix backstop,
* not a re-validation: it catches only the one class the type system cannot — a
* `__`-prefixed string force-branded to `Tag` (a raw `as Tag` cast, a harness `mkTag`,
* any hand-forged brand) that never passed through a builder. It intentionally does NOT
* re-check charset — narrow, not weak.
*/
function assertUserTag(tag) {
	if (isReservedTag(tag)) throw new InvalidTagError(tag, `user tags may not begin with the reserved "${RESERVED_TAG_PREFIX}" prefix (§2.3, MI13)`);
}
//#endregion
//#region ../../packages/cache/src/invalidation.ts
/** The one reason class a `strong_degraded` emit carries today. */
var STRONG_DEGRADED_REGISTRY_UNREACHABLE = "registry_unreachable";
/** The channel's starting state, and the one a reader returns to saying rather than assuming: it has
* never heard a `hello`. Named once so the three sites that compare against it cannot drift. */
var NEVER_ESTABLISHED = "never-established";
/** Build the `transient-miss` retry signal for `seq` (the single runtime literal source). */
function transientMiss(seq) {
	return {
		kind: "transient-miss",
		seq
	};
}
/** Narrow an `unknown` to an index-readable object, or `undefined`. The
* single narrowing assertion in this module: every field read off the result is
* individually type-checked below before use, so the record view is sound. */
function parseValueObject(value) {
	if (typeof value !== "object" || value === null) return;
	return value;
}
/** Parse a stored JSON string to an object, or `undefined` on a malformed body. */
function parseJsonObject(raw) {
	let parsed;
	try {
		parsed = JSON.parse(raw);
	} catch {
		return;
	}
	return parseValueObject(parsed);
}
/** Read the `state` pointer shape from stored bytes. Malformed — including a
* non-string or absent `regId` echo — ⇒ undefined (fail closed). */
function readPointer(raw) {
	const o = parseJsonObject(raw);
	if (!o || typeof o.epoch !== "number" || typeof o.lastBatch !== "number" || typeof o.regId !== "string") return;
	const snapBatch = typeof o.snapBatch === "number" ? o.snapBatch : 0;
	const flushedAt = typeof o.flushedAt === "number" ? o.flushedAt : 0;
	return {
		epoch: o.epoch,
		flushedAt,
		lastBatch: o.lastBatch,
		regId: o.regId,
		snapBatch
	};
}
/**
* Read one watermark change from a parsed value. A CORRUPT change ⇒ `undefined`
* (fail closed), DISTINGUISHED from a legitimately-absent optional axis:
*   • a PRESENT-but-non-number `softEpoch`/`hardEpoch` (the field is on the object but is not a
*     `number`) is corruption — dropping it and applying the rest would silently delete a purge
* from history (a serve-dead vector under non-canonical KV writes);
*   • an AXIS-EMPTY change (a `tag` but NEITHER a numeric `softEpoch` NOR a numeric `hardEpoch`)
* violates the ≥1-axis rule.
* An ABSENT optional axis stays legal — a change may legitimately touch a single axis. A corrupt
* change holes its whole batch via `readFrame` (never a partial-apply); the caller fails closed.
*/
function readChange(value) {
	const rec = parseValueObject(value);
	if (!rec) return;
	const { tag } = rec;
	if (typeof tag !== "string") return;
	const soft = rec.softEpoch;
	const hard = rec.hardEpoch;
	if ("softEpoch" in rec && typeof soft !== "number" || "hardEpoch" in rec && typeof hard !== "number") return;
	if (typeof soft !== "number" && typeof hard !== "number") return;
	return {
		tag,
		...typeof soft === "number" ? { softEpoch: soft } : {},
		...typeof hard === "number" ? { hardEpoch: hard } : {}
	};
}
/**
* Read a `__delta` batch frame from stored bytes. Malformed ⇒ undefined. Exported so the
* doBus wire codec (`src/adapters/cloudflare/wire.ts`) validates a wire-carried frame through the
* SAME frame parser the KV-resync path uses — one parser for the durable mirror AND the live channel,
* so a corrupt wire change fails closed exactly like a corrupt `__delta` byte (never a partial
* apply). Additive export only: no reader branch changes, no behavior change (the corrupt-change
* fail-closed semantics are pinned by `reader-corrupt-change-fail-closed`).
*/
function readFrame(raw) {
	const o = parseJsonObject(raw);
	if (!o || typeof o.fromEpoch !== "number" || typeof o.toEpoch !== "number" || !Array.isArray(o.changes)) return;
	const changes = [];
	for (const entry of o.changes) {
		const change = readChange(entry);
		if (!change) return;
		changes.push(change);
	}
	return {
		changes,
		fromEpoch: o.fromEpoch,
		toEpoch: o.toEpoch
	};
}
/** Merge a frame's watermark changes into a knowledge map (max-on-write). */
function applyChanges(known, frame) {
	for (const change of frame.changes) {
		const w = known.get(change.tag) ?? {
			hardEpoch: 0,
			softEpoch: 0
		};
		if (change.softEpoch !== void 0) w.softEpoch = Math.max(w.softEpoch, change.softEpoch);
		if (change.hardEpoch !== void 0) w.hardEpoch = Math.max(w.hardEpoch, change.hardEpoch);
		known.set(change.tag, w);
	}
}
/** The validity table over an entry's `bornEpoch` and per-tag knowledge. */
function classify(bornEpoch, knowledge) {
	if (knowledge.some((k) => !k.known)) return "unknown";
	if (knowledge.some((k) => k.known && k.hardEpoch > bornEpoch)) return "dead";
	if (knowledge.some((k) => k.known && k.softEpoch > bornEpoch)) return "stale";
	return "fresh";
}
/** The highest `throughEpoch` a live check verified (0 if none is `known`). */
function throughEpochOf(knowledge) {
	let max = 0;
	for (const k of knowledge) if (k.known && k.throughEpoch > max) max = k.throughEpoch;
	return max;
}
/** Default liveness heartbeat interval in ms — the cadence at which a live writer refreshes the
* pointer's freshness field. The reader mirrors the coordinator's `REGISTRY_HEARTBEAT_MS`; the
* 2b-2-b shell threads the real value at construction. Suspicion raises after `3 ×` this much
* OBSERVED SILENCE — an elapsed-time derivative, sampling-rate-independent (not a tick count). */
var DEFAULT_HEARTBEAT_INTERVAL_MS = 3e4;
var SUSPICION_SILENCE_MULTIPLE = 3;
/**
* The per-isolate invalidation state. Tracks tri-state tag knowledge
* fed by the bus, the highest contiguously-applied epoch, and a `suspect` flag
* raised on any gap/reconnect that a resync must clear before warm trust resumes.
*Tags absent from the map are `unknown` and fail closed (invariant 11).
*/
var Invalidation = class {
	#registry;
	#stateStore;
	/** The bus, HELD as an address rather than a subscription. Construction wires; it does not
	* dial. The first read that needs the channel subscribes (`#ensureSubscribed`), inside the request
	* context that asked — which is the only context where a dial is permitted to happen at all. */
	#bus;
	/** Telemetry for the reader's own raw facts — currently `bus_dial_failed`. Optional: a cache
	* built with no sink emits nothing, and the reader never depends on the emit for correctness. */
	#emit;
	/** This isolate's mirror-key scope, read from `registry.regId` at construction. It
	* both scopes the keys this reader resyncs off (`mirrorKeys(#regId)`) AND is the value the
	* pointer's echoed `regId` must equal — a mismatch reads a foreign keyspace and fails closed. */
	#regId;
	#subscription = null;
	/** `close()` was called: no further subscribe may happen (a lazy dial must not resurrect a closed
	* reader — the one way deferral could outlive its owner). */
	#closed = false;
	/** The live channel's state — see `ChannelState`. Starts at the honest answer: this reader
	* has never heard a `hello`, and until it does it says so rather than resting on silence. */
	#channel = NEVER_ESTABLISHED;
	/** tag → applied watermark; absence ⇒ `unknown` (tri-state). */
	#known = /* @__PURE__ */ new Map();
	/** Highest contiguous epoch applied from bus frames / resync. Drives gap
	* detection — must stay strictly contiguous. */
	#appliedEpoch = 0;
	/** Highest epoch this isolate has VERIFIED from any authority — the contiguous bus
	* `#appliedEpoch` OR a live check's `throughEpoch`. This is the writer's
	* applied registry epoch for bornEpoch minting: a fill after a verified
	* live check must postdate what that check observed, so a legitimate refill on a
	* behind isolate is not fenced by its own recorded watermark. */
	#knownThroughEpoch = 0;
	/** Raised on a bus gap or reset; forces resync before warm trust. While raised,
	* every tag is treated as unknown at eventual reads — a possibly-gapped map is not the
	* merely-degraded warm map Q4 lets an isolate keep serving on. */
	#suspect = false;
	/** The max head DECLARED over the currently-open live channel during THIS suspect episode
	* — from a LOSS-revealing event only: `hello.head`, `gap.head`, or a NON-CONTIGUOUS
	* frame's `toEpoch`. A contiguous frame never declares (it only reaches toward a named head).
	* `null` until a head is declared this episode; a `reset` discards it (new episode), a `gap`
	* RAISES it (≥ the frame it lost) — so a hidden purge behind a hole is accounted before
	* convergence, never skipped (serve-dead — the shape). Convergence anchors
	* to THIS, never to pointer freshness. */
	#headThisEpisode = null;
	/** The `flushedAt` of the pointer at the last observation that ADVANCED it. `null` until
	* the first observation seeds the baseline. Compared DERIVATIVELY — "did it advance?" via
	* EQUALITY/`>` — never as an absolute-freshness anchor ( : `flushedAt` may not clear
	* suspicion nor date the map). */
	#lastFlushedAt = null;
	/** The `nowMs` of the previous observation — a MONOTONICITY guard so a stale/reordered tick
	* (`nowMs ≤` this) is a true no-op, NOT an age anchor (it is never compared to `flushedAt`).
	* `null` until the first observation. */
	#lastObservationMs = null;
	/** The `nowMs` at which the current unbroken SILENCE began — the last observation that seeded
	* the baseline or saw `flushedAt` advance. `nowMs − this` is the observed silence; reaching
	* `#suspicionAfterMs` RAISES suspicion (derivative, raise-only, never clears). `null` until the
	* first observation. */
	#silenceStartMs = null;
	/** Raise suspicion once OBSERVED SILENCE reaches this — `3 × heartbeatIntervalMs`, fixed at
	* construction. Elapsed silence is sampling-rate-INDEPENDENT: fast ticks cannot witness this much
	* silence in less than this much time, so a false raise is structurally impossible. */
	#suspicionAfterMs;
	constructor(registry, stateStore, bus, heartbeatIntervalMs = DEFAULT_HEARTBEAT_INTERVAL_MS, emit) {
		this.#registry = registry;
		this.#emit = emit;
		this.#suspicionAfterMs = SUSPICION_SILENCE_MULTIPLE * heartbeatIntervalMs;
		this.#stateStore = stateStore;
		this.#regId = registry.regId;
		this.#bus = bus;
	}
	/**
	* Subscribe to the invalidation bus if this reader has not already — THE LAZY DIAL,
	* and the deferral the reachability rule requires of construction. Idempotent and synchronous, so any entry point
	* may call it unconditionally; the dial itself is the transport's business and may complete long
	* after this returns.
	*
	* CALL IT FROM EVERY PATH THAT IS "USE" — the read path (`validate`), the tick path (`resync` /
	* `resyncViaSnapshot`), and the purge verbs, which are registry RPCs and therefore already inside
	* a request context. NEVER from `explain`: a witness that dialed would be testimony rather than
	* introspection, which is why `explainVerdict` is the one reader entry point that does not
	* appear on that list.
	*
	* NOTHING IS MISSED BY SUBSCRIBING LATE. A purge published before this reader was listening is not
	* lost: every establishment handshakes `reset` → `hello{head}`, which suspends warm trust and
	* anchors a resync against the durable `__state`/`__delta` mirror. The live channel is an
	* optimization over that mirror, never the sole path — so the cost of subscribing at first use is
	* one cold resync, and the benefit is an isolate that can be built at module scope at all.
	*
	* The subscriber consumes a BusEvent union: a `reset` (transport (re)establishment) or a `gap`
	* (dropped frame) marks the map suspect and forces a resync before warm trust resumes; a `frame`
	* takes the contiguity path; a `lost` records the channel's own state.
	*/
	ensureSubscribed() {
		if (this.#subscription !== null || this.#closed) return;
		this.#subscription = this.#bus.subscribe((event) => {
			this.#applyEvent(event);
		});
	}
	get appliedEpoch() {
		return this.#appliedEpoch;
	}
	/** Whether this isolate currently DISTRUSTS its warm map — read-only. A read
	* arriving while suspect fails closed (the gate); the shell reads it to know whether
	* the ladder still has work. Raised by a bus gap/reset OR the derivative heartbeat miss;
	* cleared ONLY by `#tryConverge` reaching a live-channel head (never by pointer freshness). */
	get suspect() {
		return this.#suspect;
	}
	/** The live channel's state — read-only exposition of state this reader already tracks for
	* its own use, never new bookkeeping added for the witness. */
	get channel() {
		return this.#channel;
	}
	close() {
		this.#closed = true;
		this.#subscription?.close();
		this.#subscription = null;
	}
	/** Advance the contiguous applied epoch (and the verified high-water) to `toEpoch`. */
	#advanceApplied(toEpoch) {
		this.#appliedEpoch = toEpoch;
		this.#knownThroughEpoch = Math.max(this.#knownThroughEpoch, toEpoch);
	}
	/**
	* Dispatch one bus event. Convergence anchors to a head DECLARED this episode,
	*from exactly the LOSS-REVEALING events: a `hello` (the channel head), a `gap`
	* (its `head`, ≥ the frame it lost), or a NON-CONTIGUOUS frame (its `toEpoch`, the hole
	* it proves). A CONTIGUOUS frame does NOT declare — it only reaches toward a head already named.
	* A `reset` discards the anchor (a new episode; the following `hello` re-declares). The kernel,
	* not the bus, owns contiguity validation.
	*/
	#applyEvent(event) {
		switch (event.kind) {
			case "frame":
				this.#applyFrame(event.frame);
				return;
			case "hello":
				this.#channel = "established";
				this.#headThisEpisode = Math.max(this.#headThisEpisode ?? 0, event.head);
				this.#tryConverge();
				return;
			case "lost": {
				const healthyClose = this.#channel === "established";
				this.#channel = this.#channel === NEVER_ESTABLISHED ? NEVER_ESTABLISHED : "lost";
				if (!healthyClose) this.#emit?.({ type: TELEMETRY_EVENTS.bus_dial_failed });
				return;
			}
			case "gap":
				this.#suspect = true;
				this.#headThisEpisode = Math.max(this.#headThisEpisode ?? 0, event.head);
				return;
			case "reset":
				this.#suspect = true;
				this.#headThisEpisode = null;
				return;
			default: return event;
		}
	}
	/** Apply a bus frame if contiguous; otherwise raise suspicion. */
	#applyFrame(frame) {
		if (frame.fromEpoch !== this.#appliedEpoch) {
			this.#suspect = true;
			this.#headThisEpisode = Math.max(this.#headThisEpisode ?? 0, frame.toEpoch);
			return;
		}
		applyChanges(this.#known, frame);
		this.#advanceApplied(frame.toEpoch);
		this.#tryConverge();
	}
	/**
	* Clear suspicion iff a head was DECLARED over the live channel this episode (hello/frame)
	* AND the applied epoch has reached it. `#appliedEpoch` advances only by contiguous
	* application, so `applied >= head` already encodes hole-free reach (β) — no separate holes
	* check. Pointer freshness never arbitrates clearing: a heartbeat refreshes the pointer's
	* freshness field without minting epochs, so freshness motion is not evidence the reader has
	* caught up; only a live-channel head declaration anchors convergence. Bus-down /
	* poll-only declares no head ⇒ sticky suspect (posture, priced).
	*/
	#tryConverge() {
		if (this.#suspect && this.#headThisEpisode !== null && this.#appliedEpoch >= this.#headThisEpisode) this.#suspect = false;
	}
	/**
	* Cold/resync path — the bounded single DELTA pass. Read the `__state`
	* pointer from L2 and replay the `(applied, lastBatch]` delta batches — durably write-once
	* in L2 even when the bus frame was dropped — applying the contiguous prefix (β), which
	* advances `#appliedEpoch`. A missing pointer or a hole leaves affected knowledge `unknown`
	* (fail closed). Resync does NOT clear suspicion off pointer freshness: a heartbeat refreshes the
	* freshness field without minting epochs, so freshness motion is not evidence of catch-up.
	*It only APPLIES the durable prefix; clearing is `#tryConverge`'s job, and fires iff
	* a head was DECLARED over the live channel this episode (hello/frame) AND this replay carried
	* the applied epoch up to it.
	*
	* SURFACES A STATUS (2b-2-a): the walk's hole is no longer discarded — it is returned so the
	* SHELL (2b-2-b) can schedule the retry ladder off a pure decision. `resync` NEVER sleeps; the
	* escalation across attempts is driven by the caller's `policy.deltasExhausted`, not by time.
	*
	* THE RETRY LADDER NEVER BLOCKS A READ: `validate()`'s awaited resync stays THIS bounded fast
	* pass; a read arriving during `transient-miss` classifies `unknown` and takes the posture
	* NOW — the ladder heals the future, not the present. Fail-closed means what it says. (So
	* `validate` calls `resync()` with the default policy and ignores the status; it does not wait
	* on retries, and a `transient-miss`/`needs-snapshot`/`terminal-gap` read fails closed at once
	* via the gate.)
	*/
	async resync(policy) {
		this.ensureSubscribed();
		const deltasExhausted = policy?.deltasExhausted ?? false;
		const mirror = await this.#readMirrorPointer();
		if (!mirror) {
			this.#tryConverge();
			return transientMiss(0);
		}
		const hole = await this.#replayBatches(mirror.store, mirror.keys, 1, mirror.pointer.lastBatch);
		this.#tryConverge();
		if (hole === null) return "converged";
		return deltasExhausted ? "needs-snapshot" : transientMiss(hole);
	}
	/**
	* The SNAPSHOT bridge — the escalation the shell drives after `resync` returns
	* `needs-snapshot`. Load ONLY the pointer-blessed snapshot `__snap:<pointer.snapBatch>` (a
	* from-0 `BusFrame`, `fromEpoch:0`, parsed by the SAME `readFrame`), apply it as an ABSOLUTE
	* base (`#appliedEpoch` ← `snap.toEpoch` when ahead — bridging a hole in the now-GC-eligible
	* region below `snapBatch`), then replay the tail `(snapBatch, lastBatch]`. Never fetches an
	* UNBLESSED snap nor a delta past `lastBatch` (the over-fetch discipline the contract pins).
	*
	* A lagging `snapBatch` is FIRST-CLASS NORMAL (1c): `snapBatch` may trail `lastBatch`
	* arbitrarily — the arithmetic on `(snapBatch, lastBatch]` is correct, no warning, no anomaly.
	*
	* BLESSED-BUT-MISSING SNAP: if `__snap:<snapBatch>` is itself absent, give it ONE brief retry
	* (`transient-miss` — it may be lagging); once the caller's snapshot-budget is spent
	* (`snapshotExhausted`), rule permanently — a FULL delta replay from the floor if the chain
	* `(0, lastBatch]` is intact (⇒ `converged`), else `terminal-gap`. No spin, no serve-through.
	*/
	async resyncViaSnapshot(policy) {
		this.ensureSubscribed();
		const snapshotExhausted = policy?.snapshotExhausted ?? false;
		const mirror = await this.#readMirrorPointer();
		if (!mirror) {
			this.#tryConverge();
			return transientMiss(0);
		}
		const { store, keys, pointer } = mirror;
		const snapRaw = await store.get(keys.snapKey(pointer.snapBatch), "snap");
		const snap = snapRaw ? readFrame(snapRaw.value) : void 0;
		if (!snap) {
			if (!snapshotExhausted) {
				this.#tryConverge();
				return transientMiss(pointer.snapBatch);
			}
			const holeNoSnap = await this.#replayBatches(store, keys, 1, pointer.lastBatch);
			this.#tryConverge();
			return holeNoSnap === null ? "converged" : "terminal-gap";
		}
		this.#applySnapshotBase(snap);
		const tailHole = await this.#replayBatches(store, keys, pointer.snapBatch + 1, pointer.lastBatch);
		this.#tryConverge();
		if (tailHole === null) return "converged";
		return transientMiss(tailHole);
	}
	/**
	* Read the `__state` pointer through the registry-scoped keys, returning the scoped
	* `store`/`keys` alongside it, or `undefined` when the pointer is absent/malformed OR echoes a
	* foreign `regId` (a scheme/prefix divergence or another registry's pointer — fail closed).
	*/
	async #readMirrorPointer() {
		const store = this.#stateStore;
		const keys = mirrorKeys(this.#regId);
		const raw = await store.get(keys.stateKey, "pointer");
		const pointer = raw ? readPointer(raw.value) : void 0;
		if (!pointer || pointer.regId !== this.#regId) return;
		return {
			keys,
			pointer,
			store
		};
	}
	/**
	* Replay batches `fromSeq..lastBatch` (XRANGE discipline), applying each whose
	* `fromEpoch` is contiguous with the applied epoch. Returns the seq of the FIRST hole
	* (a missing/garbled/non-contiguous forward batch — fail closed), or `null` when the walk
	* reached `lastBatch` hole-free. `fromSeq` is `1` for a from-floor `(0, lastBatch]` replay and
	* `snapBatch + 1` for a snapshot-bridged tail `(snapBatch, lastBatch]` — so GC'd batches below
	* a snapshot floor are never miscounted as holes. Reads ONLY `deltaKey(fromSeq..lastBatch)` —
	* never a seq past the pointer — through the SAME `keys` the pointer was read with (
	* over-fetch discipline).
	*/
	async #replayBatches(store, keys, fromSeq, lastBatch) {
		let firstHole = null;
		for (let seq = fromSeq; seq <= lastBatch; seq++) {
			const batchRaw = await store.get(keys.deltaKey(seq), "delta");
			const frame = batchRaw ? readFrame(batchRaw.value) : void 0;
			const applied = frame ? this.#applyIfContiguous(frame) : false;
			if (!(frame && applied) && firstHole === null) firstHole = seq;
		}
		return firstHole;
	}
	/** Apply a batch frame iff it advances the applied epoch contiguously. */
	#applyIfContiguous(frame) {
		if (frame.toEpoch <= this.#appliedEpoch) return true;
		if (frame.fromEpoch !== this.#appliedEpoch) return false;
		applyChanges(this.#known, frame);
		this.#advanceApplied(frame.toEpoch);
		return true;
	}
	/**
	* Apply a from-0 snapshot frame as an ABSOLUTE base: merge its watermarks (max-on-write,
	* never regressing a higher already-known value) and JUMP the applied epoch to `snap.toEpoch`
	* when the snapshot is AHEAD — a complete `(0, snap.toEpoch]` checkpoint carries its own
	* contiguity from the floor, so no `fromEpoch === applied` gate applies. A snapshot BEHIND the
	* applied epoch (a lagging `snapBatch` — first-class normal) adds nothing and never regresses.
	*/
	#applySnapshotBase(snap) {
		if (snap.toEpoch <= this.#appliedEpoch) return;
		applyChanges(this.#known, snap);
		this.#advanceApplied(snap.toEpoch);
	}
	/**
	* derivative suspicion RAISING — a PURE state update fed one pointer
	* observation. A heartbeat refreshes the pointer's freshness field without minting epochs, so
	* freshness MOTION is not evidence the reader has caught up; only a live-channel head declaration
	* anchors convergence. This monitor uses freshness the OTHER way — to RAISE: it tracks OBSERVED
	* SILENCE (how long `flushedAt` has failed to advance) and raises `#suspect` once that silence
	* reaches `#suspicionAfterMs`. DERIVATIVE-ONLY, RAISE-ONLY: `flushedAt` never clears suspicion nor
	* anchors absolute freshness — only `>`/equality ACROSS observations, only to RAISE.
	* `nowMs` is compared ONLY to our own prior watch-times (`#lastObservationMs`, `#silenceStartMs`),
	* NEVER to `flushedAt`, so no absolute-age anchor sneaks in. Measuring ELAPSED silence (not
	* counting non-advancing ticks) makes the raise SAMPLING-RATE-INDEPENDENT: fast ticks cannot
	* witness `#suspicionAfterMs` of silence in less time, and a slow-but-live writer resets the
	* window. The shell feeds it observations (2b-2-b); here explicit test ticks.
	*/
	notePointerObservation(pointer, nowMs) {
		if (this.#lastObservationMs !== null && nowMs <= this.#lastObservationMs) return;
		this.#lastObservationMs = nowMs;
		if (this.#lastFlushedAt === null || this.#silenceStartMs === null) {
			this.#lastFlushedAt = pointer.flushedAt;
			this.#silenceStartMs = nowMs;
			return;
		}
		if (pointer.flushedAt > this.#lastFlushedAt) {
			this.#lastFlushedAt = pointer.flushedAt;
			this.#silenceStartMs = nowMs;
			return;
		}
		if (nowMs - this.#silenceStartMs >= this.#suspicionAfterMs) this.#suspect = true;
	}
	/**
	* The poll DECISION — a PURE, deterministic predicate: has at least `thresholdMs`
	* elapsed since the last poll? The base cadence is maxSyncLag (60s) and the ±20% jitter is the
	* SHELL's rng job (2b-2-b) — both enter as the caller-computed `thresholdMs`, so no ambient
	* time/rng lives here. The poll DRIVER (the timer that calls this) is 2b-2-b.
	*/
	pollDue(nowMs, lastPollMs, thresholdMs) {
		return nowMs - lastPollMs >= thresholdMs;
	}
	/**
	* The `bornEpoch` to stamp into a fresh envelope at fill start, read
	* SYNCHRONOUSLY from LOCAL VERIFIED knowledge — the highest epoch this isolate has
	* observed from any authority (contiguous bus applies OR a recorded live check),
	* never a fresh live read. So the default read path keeps the zero-round-trip cost
	* model, while a fill that follows a verified live check postdates what that check
	* observed — a legitimate refill on a behind isolate is not fenced by its own
	* recorded watermark. A lagging high-water can only UNDER-state bornEpoch ⇒
	* over-invalidation ⇒ the safe direction. The caller snapshots
	* this EARLY as the fence-critical floor (a concurrent bus purge landing after the
	* snapshot cannot lift it, so INV-9/12 hold) and only raises the fill's bornEpoch by
	* the epoch its OWN reads verified (via `validate`'s `checkedThrough`), never by a
	* concurrent frame — see `runGetOrSet`.
	*/
	localBornEpoch() {
		return this.#knownThroughEpoch;
	}
	/**
	* The verdict `explain` reports — a PURE read of CURRENT local knowledge: classify
	* the envelope against what this isolate knows NOW, with NO live check, NO resync, and NO
	* recording. It is exposition, not testimony: it adds no tracked state and mutates nothing (the
	* introspection-not-testimony line). A suspect map fails closed to `unknown` (the posture
	* an eventual read would take), and `unknown` stays `unknown`. The soft/hard watermarks are the
	* evidence the bornEpoch was weighed against — the purge epochs behind a `stale`/`dead` verdict
	* (`0` when nothing is known / fail-closed).
	*/
	explainVerdict(envelope) {
		const knowledge = this.#localKnowledge(envelope.tags);
		let softEpoch = 0;
		let hardEpoch = 0;
		for (const k of knowledge) if (k.known) {
			softEpoch = Math.max(softEpoch, k.softEpoch);
			hardEpoch = Math.max(hardEpoch, k.hardEpoch);
		}
		const validity = this.#suspect ? "unknown" : classify(envelope.bornEpoch, knowledge);
		return {
			hardEpoch,
			softEpoch,
			validity
		};
	}
	/**
	* Validate an envelope against this isolate's knowledge (table). `strong`
	* (or an unknown tag under `registry-check`) consults a live registry read; a
	* plain `eventual` read uses only local bus-fed knowledge and fails closed on any
	* unknown/suspect tag (invariant 11). Never serves a hard-purged value.
	*
	* REACHABILITY IS SPLIT BY CHANNEL. This method is the REGISTRY-RPC half: a failure here is
	* a strong read's `RegistryUnavailableError` or, on the eventual path, a `'miss'`.
	* The BUS half is not represented here at all — a bus that is down or never came up is never
	* grounds for a throw, because the eventual path's whole design is to be correct without it.
	*/
	async validate(envelope, consistency, resolution, onUnavailable) {
		const { tags, bornEpoch } = envelope;
		this.ensureSubscribed();
		if (consistency === "strong") try {
			return await this.#strongLiveCheck(tags, bornEpoch);
		} catch (error) {
			if (onUnavailable !== "eventual") throw new RegistryUnavailableError(`strong read could not reach the registry — ${String(error)}`);
			this.#emit?.({
				reason: STRONG_DEGRADED_REGISTRY_UNREACHABLE,
				type: TELEMETRY_EVENTS.strong_degraded
			});
		}
		if (this.#suspect) await this.resync();
		const localVerdict = this.#suspect ? "unknown" : classify(bornEpoch, this.#localKnowledge(tags));
		if (localVerdict !== "unknown") return {
			checkedThrough: 0,
			validity: localVerdict
		};
		if (typeof resolution !== "string") throw new UnknownTagError(envelope.key, this.#unknownUserTags(tags), resolution.armedBy);
		if (resolution !== "registry-check") return {
			checkedThrough: 0,
			validity: "unknown"
		};
		let checked;
		try {
			checked = await this.#registry.check(tags);
		} catch {
			return {
				checkedThrough: 0,
				validity: "unknown"
			};
		}
		this.#recordChecked(tags, checked);
		return {
			checkedThrough: throughEpochOf(checked),
			validity: classify(bornEpoch, checked)
		};
	}
	/**
	* The strong-read live check: ONE live, un-memoized registry read over
	* `tags`, memoized into local knowledge (`#recordChecked`), classified against
	* `bornEpoch` via the table. The single engine of both the strong READ arm of
	* `validate` and the pre-fetch strong-miss check (`liveCheckForFill`) —
	* local knowledge is never consulted here; the primary read is the authority.
	*/
	async #strongLiveCheck(tags, bornEpoch) {
		const knowledge = await this.#registry.check(tags);
		this.#recordChecked(tags, knowledge);
		return {
			checkedThrough: throughEpochOf(knowledge),
			validity: classify(bornEpoch, knowledge)
		};
	}
	/**
	* Pre-fetch strong-miss live check: the SAME live un-memoized
	* registry read `#strongLiveCheck` runs, executed BEFORE the factory on a strong fill so
	* the fill's bornEpoch is minted from now-current knowledge — an understatement, never an
	* overstatement, of recency (the safe direction). Records the reading into `#known` (
	* memoization), so a later eventual read of the tag fails closed with no second round-trip.
	* Returns the verified high-water; the caller raises the fill's bornEpoch to it.
	*/
	async liveCheckForFill(tags, onUnavailable) {
		try {
			const { checkedThrough } = await this.#strongLiveCheck(tags, 0);
			return checkedThrough;
		} catch (error) {
			if (onUnavailable !== "eventual") throw new RegistryUnavailableError(`strong fill could not reach the registry to mint its bornEpoch — ${String(error)}`);
			this.#emit?.({
				reason: STRONG_DEGRADED_REGISTRY_UNREACHABLE,
				type: TELEMETRY_EVENTS.strong_degraded
			});
			return 0;
		}
	}
	/** Merge a verified live-check result into local knowledge. Max-on-write, so
	* a concurrently-applied newer bus frame is never regressed. The check's
	* `throughEpoch` advances the verified high-water (bornEpoch source) so a subsequent
	* refill postdates what this check observed. */
	#recordChecked(tags, knowledge) {
		for (const [i, tag] of tags.entries()) {
			const k = knowledge[i];
			if (k === void 0 || !k.known) continue;
			const w = this.#known.get(tag) ?? {
				hardEpoch: 0,
				softEpoch: 0
			};
			w.softEpoch = Math.max(w.softEpoch, k.softEpoch);
			w.hardEpoch = Math.max(w.hardEpoch, k.hardEpoch);
			this.#known.set(tag, w);
			this.#knownThroughEpoch = Math.max(this.#knownThroughEpoch, k.throughEpoch);
		}
	}
	/**
	* The write-back fence, evaluated against LOCAL bus-applied knowledge: any tag
	* `hardEpoch > bornEpoch` in the isolate's contiguously-applied state ⇒ the candidate
	* is fenced (a hard purge landed DURING the fill and was delivered here). Returns
	* `true` when the write-back may proceed. A soft purge lands already-stale but still
	* stores; only DEAD fences.
	*
	* DESIGN (per the INV-12 erratum): the write-back guard is realized as a LOCAL
	* fence here plus read-time fail-closed resolution — not a live registry read on the
	* write path. The local fence catches a purge that landed AND delivered locally DURING the
	* fill (tag KNOWN-dead locally ⇒ INV-9/12); it lets a cold/dropped-frame isolate STORE a value
	* whose tag is locally unknown (origin is truth after a purge — the legitimate
	* refill case). A born-dead value that slips past this fence is stored but INERT: its own
	* SUBSEQUENT eventual reads fail closed on the unknown tag and live-check — that read-time
	* check is where the GOAL (never serve a value born against a durably-accepted purge) is
	* enforced for THAT path. A strong fill's OWN serve is anchored earlier, at INITIATION: the
	* pre-factory check minted its `bornEpoch` at the global epoch, so it already dominates
	* every purge accepted before initiation — no read-time check re-verifies the fill
	* that produced it.
	*/
	fenceWriteBack(tags, bornEpoch) {
		return classify(bornEpoch, this.#localKnowledge(tags)) !== "dead";
	}
	/** Local tri-state knowledge for these tags (absent ⇒ unknown). */
	/**
	* The USER tags this read could not establish — what `UnknownTagError` carries.
	*
	* Under suspicion EVERY tag is unknown (the gate treats a map with a hole as knowing
	* nothing), so the whole user set is the honest answer there. Otherwise it is exactly the tags the
	* map has no watermark for. Reserved tags are dropped either way: `__k:`/`__ns:` are the kernel's
	* own bookkeeping, they are unknown for the same reason the user's tags are, and a caller cannot
	* do anything with them — the same reason they never reach an L3 header.
	*/
	#unknownUserTags(tags) {
		const knowledge = this.#localKnowledge(tags);
		return tags.filter((tag, index) => !isReservedTag(tag) && (this.#suspect || knowledge[index]?.known !== true));
	}
	#localKnowledge(tags) {
		return tags.map((tag) => {
			const w = this.#known.get(tag);
			if (!w) return { known: false };
			return {
				hardEpoch: w.hardEpoch,
				known: true,
				softEpoch: w.softEpoch,
				throughEpoch: this.#appliedEpoch
			};
		});
	}
};
//#endregion
//#region ../../packages/cache/src/replication-poller.ts
var DEFAULT_POLL_BASE_MS = 6e4;
var DEFAULT_FIRST_LADDER_DELAY_MS = 1e3;
var DEFAULT_LATER_LADDER_DELAY_MS = 5e3;
var DEFAULT_JITTER_FRACTION = .2;
/** Poll-failure backoff ceiling (knob): a warm baseline poll that does NOT converge doubles
* the cadence (×2) up to this cap — 300s — and the doubled interval is jittered like any other, so a
* fleet whose L2 is failing degrades to one attempt per ~5 minutes per isolate instead of hammering
* a sick store every 60s. The cap is on the BASE interval; the ±jitter rides on top (a jittered
* 300s is 240–360s), because de-synchronizing the fleet matters most exactly when the store is sick. */
var MAX_POLL_BACKOFF_MS = 3e5;
/** Backoff multiplier per consecutive failed baseline poll (×2). */
var POLL_BACKOFF_FACTOR = 2;
/** The rung at which the delta budget is spent and the pass may escalate to the snapshot bridge. */
var ESCALATE_AT_ATTEMPT = 3;
/** The rung at which the snapshot budget is ALSO spent — the reader makes the permanent ruling
* (full replay if the chain is intact ⇒ converged, else terminal-gap). Below it, a lagging snap
* gets one brief retry. */
var SNAPSHOT_FINAL_ATTEMPT = 4;
/** Parse the `__state` pointer bytes the reader resyncs off into the wire shape the
* monitor consumes. Malformed / absent ⇒ `undefined` (the observation is simply skipped — a shell
* read that cannot see the pointer never fabricates liveness). Numbers-only fields; missing
* optionals default the same way the kernel reader does. */
function readStatePointer(raw) {
	let parsed;
	try {
		parsed = JSON.parse(raw);
	} catch {
		return;
	}
	if (typeof parsed !== "object" || parsed === null) return;
	const o = parsed;
	if (typeof o.epoch !== "number" || typeof o.lastBatch !== "number" || typeof o.regId !== "string") return;
	return {
		epoch: o.epoch,
		flushedAt: typeof o.flushedAt === "number" ? o.flushedAt : 0,
		lastBatch: o.lastBatch,
		regId: o.regId,
		snapBatch: typeof o.snapBatch === "number" ? o.snapBatch : 0
	};
}
/**
* The tick-driven replication poll driver. Each `onPollTick(nowMs)`:
*   1. reads the `__state` pointer and feeds the derivative liveness monitor
*      (`notePointerObservation`) — raise-only, every tick, sampling-rate-independent;
*   2. while the reader is SUSPECT, runs the retry ladder when an attempt is DUE:
* attempt 1 → `resync()` (delta); +~1s → attempt 2 → `resync()` (delta); +~5s →
*        attempt 3 → `resync({deltasExhausted})` and, on `needs-snapshot`, `resyncViaSnapshot()`
*        (snapshot-permitted). `converged` clears the reader (and backs the ladder off to the
*        baseline cadence when suspicion persists awaiting a live-channel head — never a spin);
*        `terminal-gap` STOPS the ladder permanently (fail-closed, no spin);
*   3. while WARM, runs the baseline liveness poll on the maxSyncLag cadence (60s ±20%), gated by
* the kernel's pure `pollDue`, and BACKS OFF on failure: a non-converged poll doubles the
*      interval (×2, cap 300s, still jittered); a converged one resets it to the base.
*
* Pure + tick-driven: no wall clock, no ambient timers, no sleeps — `nowMs` and the injected rng
* are the only sources of time/randomness.
*
* RE-ENTRANCY-SAFE BY CONTRACT. `onPollTick` is NOT required to be awaited before the
* next one is fired, and under the request-piggyback driver it routinely is not — a burst of
* requests hands one isolate several ticks while a step's store I/O is still in flight. Two
* properties make that safe HERE, at the root, rather than in each driver: (i) every due-decision
* and the state writes it authorizes happen in ONE synchronous block — no `await` separates a
* due-check from its commit, so no tick ever acts on a schedule another tick has already consumed;
* (ii) at most ONE ladder step is in flight, and a tick arriving mid-step declines rather than
* queues. The `__state` observation is exempt from both — the +25 vigil runs on EVERY tick.
*/
var ReplicationPoller = class {
	#reader;
	#store;
	#keys;
	#rng;
	#pollBaseMs;
	#firstLadderDelayMs;
	#laterLadderDelayMs;
	#jitterFraction;
	/** Active hole-chasing ladder, or `null` when warm (no hole to chase). */
	#ladder = null;
	/** A terminal-gap verdict indexed to the pointer that produced it: the LADDER stays stopped
	* (fail-closed, no spin) while this chain cannot complete. Permanence attaches to the CHAIN, not
	* the vigil — the baseline observation never stops (below). Cleared on a fresh suspect episode OR
	* when a durable `snapBatch` advance past `#terminalSnapBatch` bridges the hole. */
	#terminal = false;
	/** The pointer `snapBatch` the terminal verdict was made against — expired deltas never return, so
	* ONLY an observed `snapBatch` beyond this can re-arm one snapshot-permitted attempt. `null` when
	* not terminal. */
	#terminalSnapBatch = null;
	/** Suspect state at the previous tick — the false→true EDGE opens a fresh episode. */
	#prevSuspect = false;
	/** SINGLE-FLIGHT: a ladder step is between its due-decision and its awaits. Set in the
	* SAME synchronous block as the due-decision that authorized it and cleared in that block's
	* `finally`, so at most one step ever runs. Ticks are request-piggybacked (an isolate gets no
	* free-running timer), which makes RE-ENTRY ROUTINE rather than exotic — a burst of requests can
	* hand this poller several ticks while one step's store I/O is still in flight. A tick that finds
	* a step running DECLINES; it never queues, because queueing merely defers the burst it exists to
	* prevent. Nothing is lost: the rung it would have run is already scheduled (below). */
	#stepInFlight = false;
	/** Last baseline poll time (`null` until the first warm tick seeds the cursor). */
	#lastPollMs = null;
	/** Jittered baseline threshold, recomputed each poll (the current interval ±20%). */
	#pollThresholdMs;
	/** The CURRENT baseline interval the threshold is drawn from: `#pollBaseMs` while polls converge,
	* doubled per consecutive failure up to `MAX_POLL_BACKOFF_MS`. Reset to the base by the
	* first poll that converges again — the backoff is a property of the current failure episode, never
	* a permanent cadence change. */
	#pollIntervalMs;
	constructor(reader, store, keys, rng, options = {}) {
		this.#reader = reader;
		this.#store = store;
		this.#keys = keys;
		this.#rng = rng;
		this.#pollBaseMs = options.pollBaseMs ?? DEFAULT_POLL_BASE_MS;
		this.#firstLadderDelayMs = options.firstLadderDelayMs ?? DEFAULT_FIRST_LADDER_DELAY_MS;
		this.#laterLadderDelayMs = options.laterLadderDelayMs ?? DEFAULT_LATER_LADDER_DELAY_MS;
		this.#jitterFraction = options.jitterFraction ?? DEFAULT_JITTER_FRACTION;
		this.#pollIntervalMs = this.#pollBaseMs;
		this.#pollThresholdMs = this.#jitter(this.#pollIntervalMs);
	}
	/**
	* Whether the reader's replication has reached a permanent terminal gap — the
	* hole-chasing ladder is stopped, fail-closed, until a durable `snapBatch` advance re-arms it.
	* Read-only exposition of the already-tracked `#terminal` verdict for `explain`; it
	* adds no bookkeeping — the field is written only by the tick machinery, never by this getter.
	*/
	get terminal() {
		return this.#terminal;
	}
	/**
	* Advance the state machine one tick. Always observes the pointer for liveness; then
	* drives the ladder (suspect) or the baseline poll (warm). Out-of-band — never on a read path.
	*/
	async onPollTick(nowMs) {
		const raw = await this.#store.get(this.#keys.stateKey);
		const pointer = raw === void 0 ? void 0 : readStatePointer(raw.value);
		if (pointer !== void 0) this.#reader.notePointerObservation(pointer, nowMs);
		if (this.#stepInFlight) return;
		const { suspect } = this.#reader;
		if (suspect && !this.#prevSuspect) {
			this.#terminal = false;
			this.#terminalSnapBatch = null;
			this.#ladder = null;
		}
		this.#prevSuspect = suspect;
		if (this.#terminal) {
			if (pointer !== void 0 && this.#terminalSnapBatch !== null && pointer.snapBatch > this.#terminalSnapBatch) {
				this.#terminal = false;
				this.#terminalSnapBatch = null;
				if (await this.#reader.resyncViaSnapshot({ snapshotExhausted: true }) === "terminal-gap") {
					this.#terminal = true;
					this.#terminalSnapBatch = pointer.snapBatch;
				}
			}
			return;
		}
		if (suspect) {
			const ladder = this.#ladder ?? {
				attempt: 0,
				nextDueMs: nowMs
			};
			this.#ladder = ladder;
			if (nowMs < ladder.nextDueMs) return;
			ladder.attempt += 1;
			ladder.nextDueMs = nowMs + this.#jitter(this.#ladderDelay(ladder.attempt));
			this.#stepInFlight = true;
			try {
				await this.#runLadderStep(nowMs, ladder, pointer?.snapBatch ?? 0);
			} finally {
				this.#stepInFlight = false;
			}
			return;
		}
		this.#ladder = null;
		if (this.#lastPollMs === null) {
			this.#lastPollMs = nowMs;
			this.#pollThresholdMs = this.#jitter(this.#pollIntervalMs);
			return;
		}
		if (this.#reader.pollDue(nowMs, this.#lastPollMs, this.#pollThresholdMs)) {
			this.#lastPollMs = nowMs;
			const status = await this.#reader.resync();
			this.#pollIntervalMs = status === "converged" ? this.#pollBaseMs : this.#backedOff(this.#pollIntervalMs);
			this.#pollThresholdMs = this.#jitter(this.#pollIntervalMs);
		}
	}
	/** One backoff step: ×2, capped at 300s (never below the configured base — a base ABOVE the cap is
	* already slower than any backoff could make it, so the cap floors to it rather than SPEEDING the
	* poll up on failure). */
	#backedOff(intervalMs) {
		const cap = Math.max(MAX_POLL_BACKOFF_MS, this.#pollBaseMs);
		return Math.min(intervalMs * POLL_BACKOFF_FACTOR, cap);
	}
	/**
	* Run one ladder rung. Rungs 1–2 retry the delta pass; rung 3 escalates
	* (`deltasExhausted`) and bridges via the snapshot on `needs-snapshot`; rung ≥4 spends the
	* snapshot budget so the reader makes the permanent ruling. Never sleeps.
	*
	* the caller has ALREADY claimed the rung (`ladder.attempt`) and committed its provisional
	* next-due, synchronously, before this is entered — so this method only ever OVERWRITES that
	* schedule when the outcome calls for a different one. It re-schedules NOTHING on the retry path
	* (the provisional IS the retry), and it writes nothing that a re-entrant tick could race, because
	* the in-flight flag the caller set means no other tick is deciding anything while this runs.
	*/
	async #runLadderStep(nowMs, ladder, observedSnapBatch) {
		const { attempt } = ladder;
		let status = await this.#reader.resync({ deltasExhausted: attempt >= ESCALATE_AT_ATTEMPT });
		if (status === "needs-snapshot") status = await this.#reader.resyncViaSnapshot({ snapshotExhausted: attempt >= SNAPSHOT_FINAL_ATTEMPT });
		if (status === "converged") {
			ladder.attempt = 0;
			ladder.nextDueMs = nowMs + this.#jitter(this.#pollBaseMs);
			return;
		}
		if (status === "terminal-gap") {
			this.#terminal = true;
			this.#terminalSnapBatch = observedSnapBatch;
			this.#ladder = null;
		}
	}
	/** The base ladder delay for the rung just run: attempt 1 → ~1s, attempts ≥2 → ~5s. */
	#ladderDelay(attempt) {
		return attempt <= 1 ? this.#firstLadderDelayMs : this.#laterLadderDelayMs;
	}
	/** Jitter a base delay symmetrically by ±`jitterFraction`, drawn from the injected rng (no
	* ambient randomness). rng.next() ∈ [0,1) ⇒ factor ∈ [1−f, 1+f). */
	#jitter(baseMs) {
		const f = this.#jitterFraction;
		return baseMs * (1 - f + 2 * f * this.#rng.next());
	}
};
/** The library's default safe-mode codec. Deterministic JSON round-trip. */
var safeCodec = {
	decode(value) {
		return JSON.parse(value);
	},
	encode(value) {
		return JSON.stringify(value);
	},
	id: "safe/1"
};
//#endregion
//#region ../../packages/cache/src/cache.ts
/** The `<scope>` literal of the one shared, principal-free storage class. It is
* the ONLY value that keeps a response shared-cacheable under. */
var PUBLIC_SCOPE = "pub";
/** The scope evidence a served HIT carries: its stored literal, or the fail-closed
* `unreadable` class when the shared surface returned none. */
function hitScopeEvidence(hit) {
	if (hit.scopeUnreadable || hit.rawScope === void 0) return { kind: "unreadable" };
	return {
		kind: "readable",
		literal: hit.rawScope
	};
}
/** The scope evidence a FILL carries: the literal this kernel just resolved for it.
*Never `unreadable` — a computed scope cannot fail to be read. */
function fillScopeEvidence(literal) {
	return {
		kind: "readable",
		literal
	};
}
/** Record one consumed dependency on the ambient collector, if there is one. */
function recordDependency(ambient, dependency) {
	ambient?.()?.record(dependency);
}
/** Record a SERVED stored entry as a consumed dependency: the tags it was stored
* with, plus the scope evidence read off the surface it came from — `unreadable` when that
* surface returned no scope at all, which poisons the response rather than guessing. A
* `undefined` hit is a serve that never happened, and records nothing. */
function recordServedHit(ambient, hit) {
	if (hit === void 0) return;
	recordDependency(ambient, {
		scope: hitScopeEvidence(hit),
		tags: hit.envelope.tags
	});
}
/** The verdict for a registry-absent instance: with no watermark authority every
* stored entry reads FRESH, exactly as the codec-only read path classifies it — there is nothing
* that could purge it, so nothing is `stale`/`dead`/`unknown`. */
var FRESH_VERDICT = {
	hardEpoch: 0,
	softEpoch: 0,
	validity: "fresh"
};
/**
* The request-scoped per-dependency attribution `explain` surfaces — the bound
* render collector's recorded dependencies, projected to the public shape. `undefined` when no
* collector is bound (outside an adapter request frame): the attribution lives and dies with the
* request, so its ABSENCE is itself the honest report (a witness reports what it knows; unbound ⇒
* there is nothing to attribute).
*/
function explainAttribution(ambient) {
	const state = ambient?.();
	if (state === void 0) return;
	return state.records.map((dependency) => ({
		scope: dependency.scope,
		tags: dependency.tags,
		...dependency.l3 === void 0 ? {} : { l3: dependency.l3 }
	}));
}
/** Per-instance namespace-version counter. `clear()` bumps it so
* every canonical key minted AFTER the bump is unreachable from a pre-clear key —
* a second, independent defense alongside the `__ns` hard watermark (INV-23). Held
* as a small mutable cell (not a plain closure variable) so it can be threaded
* through the free functions below exactly like `Invalidation`. */
var NsVersion = class {
	#version = 0;
	get current() {
		return this.#version;
	}
	bump() {
		this.#version += 1;
	}
};
/** Canonical key form `<ns-version>:<ns>:<scope>:<user-key>`. */
function canonicalKey(nsVersion, namespace, scope, userKey) {
	return `${nsVersion}:${namespace}:${scope}:${userKey}`;
}
/**
* The adapter-visible identity fields a default-scope request can carry, in
* precedence order ( principal-derived scoping; adapter-visible
* identity / request-identity-header values). `userId` is the per-user
* principal the RR/Workers adapter surfaces; a request that carries only a
* `tenant` still bears adapter-visible identity, so it too derives a principal.
* A request with none of these carries no derivable principal.
*/
var PRINCIPAL_FIELDS = ["userId", "tenant"];
/**
* Derive the adapter-visible principal from a request. Returns
* the first present identity field's value keyed by its field name, or `undefined`
* when the request carries no recognized identity (no context / null principal ⇒
* shared `pub` class). Adapter-visible identity is a primitive
* (a userId/tenant string, a header value) — a non-primitive field value is not a
* clean principal and is skipped. A read here goes through the caller-supplied
* request object directly (NOT the dev-mode guarded Proxy) — principal derivation
* is the kernel's own inspection, not a public-factory closure, so it must never
* itself trip the guarded-accessor demotion (INV-4 test 2).
*/
function derivePrincipal(request) {
	if (request === void 0) return;
	for (const field of PRINCIPAL_FIELDS) {
		const value = request[field];
		if (typeof value === "string" || typeof value === "number" || typeof value === "boolean" || typeof value === "bigint") return `${field}:${value.toString()}`;
	}
}
/**
* Hash a derived principal into the `usr:<hash>` scope suffix. A
* synchronous SHA-256 truncated to 128 bits (32 hex) — hashing is not randomness,
*and this keeps the fill path synchronous. The result is
* non-empty and checkable, so the abolished hashless `usr:` literal can
* never recur: every `usr:` scope carries a principal hash.
*/
function hashPrincipal(principal) {
	return createHash("sha256").update(principal).digest("hex").slice(0, 32);
}
/**
* The `<scope>` literal prefix of the per-principal storage class — the ONE
* class `resolveScope` confines to the isolate-local L1. Shared by both directions so they
* cannot drift: the MINT below builds a literal with it, and `storedScopeIsL1Only` reads a
* STORED literal back through it. A serve derives confinement from the identity the entry was
* stored under, so the two sides must agree on one prefix by construction.
*/
var PRINCIPAL_SCOPE_PREFIX = "usr:";
function resolveScope(input) {
	const { scope } = input;
	if (scope === "public") return {
		l1Only: false,
		literal: "pub"
	};
	if (scope !== void 0 && typeof scope === "object") return {
		l1Only: false,
		literal: `ten:${hashPrincipal(scope.tenant)}`
	};
	const principal = derivePrincipal(input.request);
	if (principal !== void 0) return {
		l1Only: true,
		literal: `${PRINCIPAL_SCOPE_PREFIX}${hashPrincipal(principal)}`
	};
	return {
		l1Only: false,
		literal: "pub"
	};
}
/**
* Whether a STORED `<scope>` literal names the L1-confined class — the read-back twin
* of `resolveScope`'s `l1Only`, over a literal rather than the call inputs a serve no longer has.
* `pub`/`ten:` are shared-tier classes; only `usr:<hash>` is confined.
*
* NOTE the asymmetry this CANNOT see, by construction: a `pub` fill DEMOTED at fill time by the
* dev-mode guarded-accessor Proxy stores the `pub` literal, so its confinement leaves no trace in
* the stored identity. Such an entry reads back as `pub` here, and a serve reports its durability
* ABSENT — unknowable, which is the honest answer, rather than a `true` the literal cannot
* support.
*/
function storedScopeIsL1Only(literal) {
	return literal.startsWith(PRINCIPAL_SCOPE_PREFIX);
}
/** The `<scope>` literal alone — for the contexts (canonical key, join
* identity, key-selector purge) that need only the string, not the tier policy. */
function scopeLiteral(input = {}) {
	return resolveScope(input).literal;
}
/** Normalize the config's `telemetry` (a bare sink function, a config object, or
* absent) to `{ sink, hosted?, salt?, onSinkError? }`. A bare function carries no
* hosted/salt/onSinkError; the config-object form passes them through unchanged. */
function telemetryOf(config) {
	const { telemetry } = config;
	if (telemetry === void 0) return;
	if (typeof telemetry === "function") return { sink: telemetry };
	return telemetry;
}
/**
* Pseudonymize one string field for a HOSTED sink: a per-project
* HMAC-salted hash, so a short or low-entropy identifier (a key or a tag) is not
* offline-invertible and never travels raw. The salt is the per-project secret
* (`hostedTelemetry(salt)`), so holding key+tag fixed and changing ONLY the salt
* changes the ref — and the ref tracks its own input, so a key ref moves iff the
* key moves and a tag ref moves iff the tag moves (INV-15). HMAC (not a bare hash)
* so the salt genuinely keys the digest per project.
*/
function saltedRef(salt, value) {
	return createHmac("sha256", salt).update(value).digest("hex").slice(0, 32);
}
/** Redact one event field for a hosted sink: HMAC-salt every string (per-field,
* including inside a string array — key ref, tag refs), leave non-string scalars
* intact. `type` (the event NAME, not sensitive) is redacted by the caller's field
* filter, never here. A field-total salted pseudonymization — not
* anonymization. */
function redactField(salt, value) {
	if (typeof value === "string") return saltedRef(salt, value);
	if (Array.isArray(value)) return value.map((element) => redactField(salt, element));
	return value;
}
/**
* Emit a telemetry event. A plain sink receives the event verbatim
* (raw key/tag/detail — redaction is hosted-only by design, INV-15). A HOSTED sink
* receives a field-total salted pseudonymization: every string field EXCEPT `type`
* (the event name) is replaced by a per-project HMAC-salted hash of its own value,
* so no raw key/tag/salt/detail ever leaves the process. A hosted config
* missing a salt cannot pseudonymize, so it is suppressed rather than leaking raw
* fields. The final delivery — plain or redacted — goes through `safeEmit`, so an
* app sink that throws or rejects is swallowed (its `onSinkError` hook notified) and
* can never surface on the kernel's own path.
*/
function emitTelemetry(config, event) {
	const telemetry = telemetryOf(config);
	if (telemetry === void 0) return;
	if (telemetry.hosted !== true) {
		safeEmit(telemetry.sink, event, telemetry.onSinkError);
		return;
	}
	const { salt } = telemetry;
	if (salt === void 0) return;
	const redacted = { type: event.type };
	for (const [field, value] of Object.entries(event)) {
		if (field === "type") continue;
		redacted[field] = redactField(salt, value);
	}
	safeEmit(telemetry.sink, redacted, telemetry.onSinkError);
}
/** The reserved per-key tag: `__k:<h>`, `<h>` a ≥128-bit (32 hex char) digest
* of the canonical key (scoped, ns-versioned). SHA-256 truncated to 128 bits — a
* synchronous digest (`node:crypto`, available on both Node and Bun) so the fill
* path stays fully synchronous; hashing is not randomness. `expire
* ({key})` recomputes this same digest to target the identical tag. */
function keyTag(canonical) {
	return `__k:${createHash("sha256").update(canonical).digest("hex").slice(0, 32)}`;
}
/** The reserved per-namespace tag: `__ns:<name>`. Every envelope
* carries it so `clear()`'s hard purge rides the same epoch/bus/conservative
* machinery as any other purge — this is what lets a lagging isolate's read
* fail closed instead of serving pre-clear data (INV-23). */
function nsTag(namespace) {
	return `__ns:${namespace}`;
}
/** Validate the committed user-tag SET at the write boundary. Two ceilings, both
* per typed `InvalidTagError`, never `NotImplementedError`:
*   - the `__`-prefix backstop, per tag — a forged reserved tag never reaches storage;
*   - the count ceiling, over the whole set — at most `MAX_USER_TAGS` user tags.
* The caller passes whatever set is being committed — the declared tags at `getOrSet`'s
* boundary, or the settle-union of declared + `setTags` + `dependsOn` — so the ceiling
* is route-independent by construction: one function, one set, one boundary. The reserved
* `__k`/`__ns` tags are stamped separately and never counted here. The set is expected already
* deduped (the ceiling counts distinct user tags, not raw call arity). */
function validateUserTags(tags) {
	if (tags === void 0) return;
	if (tags.length > 126) throw new InvalidTagError(tags[126] ?? "", `an entry may not carry more than 126 user tags (§2.3, MI13); got ${tags.length}`);
	for (const tag of tags) assertUserTag(tag);
}
/** `tags` are sorted, deduped, and include `__k`/`__ns`. Dedupes FIRST, then validates the
* distinct set — so the count ceiling measures distinct user tags, not raw call
* arity — and returns just the caller-supplied set, sorted and deduped. The reserved
* `__k`/`__ns` tags are appended separately by `envelopeTags` below, since they depend on the
* canonical key / namespace, not just the call options. */
function sortedUserTags(tags) {
	if (tags === void 0) return [];
	const distinct = [...new Set(tags)].toSorted();
	validateUserTags(distinct);
	return distinct;
}
/** The full stored-envelope tag set: sorted user tags plus the reserved
* `__k:<h>` (per-key digest of the canonical key) and `__ns:<name>` (per-namespace
* watermark) tags. This is what rides the dual-epoch invalidation core, so
* `expire({key})` / `clear()` invalidate through the same machinery as any tag
* purge. */
function envelopeTags(userTags, canonical, namespace) {
	return [
		...userTags,
		keyTag(canonical),
		nsTag(namespace)
	].toSorted();
}
function makeTagAccumulator(declared) {
	const membership = [];
	let authored;
	let settled = false;
	return {
		isSettled() {
			return settled;
		},
		markSettled() {
			settled = true;
		},
		addMembership(tag) {
			assertUserTag(tag);
			membership.push(tag);
		},
		author(tags) {
			for (const tag of tags) assertUserTag(tag);
			authored = [...tags];
		},
		settleUserTags() {
			return [.../* @__PURE__ */ new Set([...authored ?? declared, ...membership])].toSorted();
		}
	};
}
/** Resolve the codec, falling back to the built-in safe-mode codec when the
* config wires none — `world()` wires no codec, so the core needs a default
* to store/read values at all. */
function codecOf(config) {
	return config.codec ?? safeCodec;
}
/**
* The consistency this call reads at: per-call, then the instance's declared default, then
* the own (`eventual`).
*
* The middle step is what makes `CacheDefaults.consistency` a setting rather than a decoration:
* without it, a caller who declared `defaults: { consistency: 'strong' }` got eventual reads and
* nothing said so. The final fallback is unchanged and matches what every shipped default already
* declares, so an instance that sets nothing reads exactly as it did before.
*/
function consistencyOf(config, options) {
	return options.consistency ?? config.defaults?.consistency ?? "eventual";
}
/**
* How fail-closed `unknown` resolves for this instance.
*
* `takedownSensitive` is an OVERRIDE, and the precedence is the whole point. It outranks an
* explicitly-set `unknownPolicy` rather than merely filling in for an absent one — because as an
* absent-policy default it was unreachable in practice: any composition that sets `unknownPolicy`
* shadows it, and `createWorkersCache`'s own safe posture sets it, so the switch was dead on the
* entire hero path. A legal-embargo control that a routine, recommended default silently disables is
* worse than no control: it reads as protection and does nothing.
*
* Reading it as an override is also what the flag MEANS. `unknownPolicy` says how this namespace
* would like unknowns resolved; `takedownSensitive` says this namespace is one where guessing is not
* allowed. The second is a statement about the CONTENT, so it cannot be outranked by a preference.
*/
function unknownResolution(config) {
	if (config.takedownSensitive === true || config.defaults?.takedownSensitive === true) return {
		armedBy: "takedownSensitive",
		kind: "error"
	};
	const policy = config.defaults?.unknownPolicy ?? "registry-check";
	return policy === "error" ? {
		armedBy: "unknownPolicy",
		kind: "error"
	} : policy;
}
/** The `staleIfError` predicate for this instance — the configured
* override, or the default HTTP classifier. */
function staleIfErrorOf(config) {
	return config.defaults?.staleIfError ?? isRetriableHttp;
}
/** Whether a thrown error is the `not_found` fact (a 404) — never masked,
* never retriable, but eligible for negative caching when `notFoundTtl` is declared. */
function isNotFound(err) {
	return err instanceof HttpError && err.status === 404;
}
/** A miss with no live check performed (absent / codecId / decode). */
var MISS_READ = { checkedThrough: 0 };
/** Whether a stored entry's codec identity matches (or is an accepted migration
* source) — checked BEFORE decode so a foreign codec never runs. */
function codecIdMatches(storedCodecId, codec, config) {
	if (storedCodecId === codec.id) return true;
	const acceptCodecs = config.defaults?.acceptCodecs ?? [];
	return typeof storedCodecId === "string" && acceptCodecs.includes(storedCodecId);
}
/** Reconstruct the envelope shell from stored bytes + metadata. `bornEpoch`/
* `tags` ride in metadata so validation runs without a second decode. */
function envelopeFromStore(key, raw, metadata, decoded, codecId) {
	const bornEpoch = typeof metadata.bornEpoch === "number" ? metadata.bornEpoch : 0;
	const bornMs = typeof metadata.bornMs === "number" ? metadata.bornMs : 0;
	const tags = Array.isArray(metadata.tags) ? metadata.tags.filter((t) => typeof t === "string") : [];
	return {
		bornEpoch,
		bornMs,
		codecId,
		enc: "json",
		grace: 0,
		key,
		kind: metadata.kind === "neg" ? "neg" : "val",
		scope: typeof metadata.scope === "string" ? metadata.scope : "pub",
		size: raw.length,
		src: "factory",
		storedAt: bornMs,
		tags,
		ttl: 0,
		v: 3,
		val: decoded,
		...typeof metadata.fence === "number" ? { fence: metadata.fence } : {}
	};
}
/**
* The value a HIT on this envelope SURFACES.
*
* A `kind:'neg'` envelope is a remembered 404: it caches the not-found FACT, and the envelope gives it no
* value to carry. Its stored bytes are a JSON-representable placeholder (`writeNegEnvelope`) chosen
* only because `undefined` is not a storable body — they are never the answer. So a read of a neg
* surfaces `undefined`, which is EXACTLY what the fill turn that wrote it surfaced (INV-7): the
* point of caching a 404 is that the next reader gets the same answer without paying the origin,
* and an answer that changes on the second call is not a cached fact at all. Serving the placeholder
* instead handed the caller a `null` no origin ever produced, and reported a durable VALUE hit for
* an entry holding only a negative.
*
* A `val` envelope surfaces its decoded value unchanged. This is the ONE point where the two kinds
* diverge on the read path; every other neg rule is already a refusal to serve at all ( a
* stale neg is never grace/SWR-served, so the only servable neg is a fresh one).
*/
function servedValue(envelope) {
	if (envelope.kind === "neg") return;
	return envelope.val;
}
/**
* Classify a rejected `Store.get` — the read-side twin of `classifyWriteBackFailure`, and
* consistent with its reasoning rather than merely its shape:
*
*   - CLASSIFIED (`throttled` / `unavailable`) ⇒ returned to the caller, which absorbs it. The kernel signs
*     that a suppressed L2 GET error is a plain MISS — never routed to unknown/fail-closed — so on
* the serving path the caller refills, and a configured L1 (checked FIRST) may already
*     have served without this tier being read at all.
*   - UNCLASSIFIED ⇒ RETHROWN unchanged, for the same reason the write half rethrows one: the kernel
*     absorbs only failures a driver has named, and a rejection nobody classified is not evidence of
*     absence. Swallowing it would turn an unknown fault into a confident "not found" — which, on a
*     read, is the one answer that silently refills over whatever is really there.
*
* Suppression is the only telemetry-bearing outcome (`store_read_suppressed`, a raw fact at the
* instant it occurs). A rethrown error needs no event: it reaches the caller intact.
*/
function classifyStoreReadFailure(config, key, error) {
	if (!isStoreReadError(error)) throw error;
	emitTelemetry(config, {
		code: error.code,
		key,
		type: TELEMETRY_EVENTS.store_read_suppressed
	});
	return error;
}
/** The ONE spelling of the "the store did not answer" discriminant, shared by the probe's outcome
* and the witness's own arm so the two can never drift apart. */
var READ_FAILED_KIND = "read-failed";
/** The two probe outcomes that carry no payload — one shared instance each, exactly as `MISS_READ`
* is for the read path. */
var PROBE_ABSENT = { kind: "absent" };
var PROBE_READ_FAILED = { kind: READ_FAILED_KIND };
/**
* A NON-mutating presence probe for `explain`: read `canonical` from `store` with a
* plain `store.get` — never a hydrating read — and reconstruct its envelope plus the RAW stored
* scope; `absent` when the entry is missing, codec-mismatched, or undecodable; `read-failed`
* when the store's own read was suppressed. The read fails closed exactly as the serve path
* does (INV-8/27); it observes, it never writes.
*/
async function probeStoredEntry(store, codec, config, canonical) {
	let stored;
	try {
		stored = await store.get(canonical);
	} catch (error) {
		classifyStoreReadFailure(config, canonical, error);
		return PROBE_READ_FAILED;
	}
	if (!stored || !codecIdMatches(stored.metadata?.codecId, codec, config)) return PROBE_ABSENT;
	let decoded;
	try {
		decoded = codec.decode(stored.value);
	} catch {
		return PROBE_ABSENT;
	}
	const envelope = envelopeFromStore(canonical, stored.value, stored.metadata ?? {}, decoded, codec.id);
	const storedScope = stored.metadata?.scope;
	const scope = typeof storedScope === "string" ? {
		kind: "readable",
		literal: storedScope
	} : { kind: "unreadable" };
	const storedTtl = stored.metadata?.ttl;
	return {
		envelope,
		kind: "found",
		scope,
		ttl: typeof storedTtl === "number" ? {
			kind: "stored",
			seconds: storedTtl
		} : { kind: "not-stored" }
	};
}
/**
* THE READ PATH'S CLASSIFIER for the SERVING path. Given raw stored bytes and metadata, it answers
* what a reader on THIS instance would make of them: an envelope, or nothing at all. (The
* introspection probe still runs its own copy of this sequence — see `probeStoredEntry`, which
* answers a different question and must report the causes of absence rather than collapse them to
* one. Folding the two is worth doing and is not this change.)
*
* Two gates precede the reconstruction and both fail CLOSED, because a reader that cannot identify
* or decode an entry is served a miss rather than a guess — the codecId check (INV-27/ : a
* mismatch `acceptCodecs` does not admit is a miss) and `decode` itself (INV-8: a body this codec
* cannot read is a miss). Only past both is an envelope built.
*
* Both seats call THIS function, by identity, and that is the point. "Servable" is defined by the
* read path, so the write layer's neg-write guard must ask the READER's question rather than a looser one
* of its own: an envelope no reader can classify as a val is not a servable good value — it is a
* miss wearing bytes, and protecting it protects a ghost. Answering that question with a partial
* copy of these steps is exactly how two seats stop meaning one thing while each keeps passing its
* own tests.
*/
function readerEnvelope(config, codec, key, stored) {
	if (!codecIdMatches(stored.metadata?.codecId, codec, config)) return;
	let decoded;
	try {
		decoded = codec.decode(stored.value);
	} catch {
		return;
	}
	return envelopeFromStore(key, stored.value, stored.metadata ?? {}, decoded, codec.id);
}
/**
* Read one tier: codecId-check + decode + validate against knowledge.
* `hit` is a `ReadHit` (fresh or stale-servable) or `undefined` (absent / dead /
* decode failure / codecId mismatch / suppressed read failure / unknown-fail-closed ⇒ the
* caller refills). `checkedThrough` is the epoch a live check by THIS read verified (0 if
* none), which the refill uses so a behind isolate does not fence its own legitimate refetch.
*/
async function readTier(store, codec, config, inval, consistency, key) {
	let stored;
	try {
		stored = await store.get(key);
	} catch (error) {
		classifyStoreReadFailure(config, key, error);
		return MISS_READ;
	}
	if (!stored) return MISS_READ;
	const envelope = readerEnvelope(config, codec, key, stored);
	if (!envelope) return MISS_READ;
	const storedScope = stored.metadata?.scope;
	const rawScope = typeof storedScope === "string" ? storedScope : void 0;
	const scopeEvidence = {
		scopeUnreadable: rawScope === void 0,
		...rawScope === void 0 ? {} : { rawScope }
	};
	if (!inval) return {
		checkedThrough: 0,
		hit: {
			...scopeEvidence,
			envelope,
			stale: false,
			value: servedValue(envelope)
		}
	};
	const { validity, checkedThrough } = await inval.validate(envelope, consistency, unknownResolution(config), config.defaults?.onUnavailable);
	if (validity === "fresh") return {
		checkedThrough,
		hit: {
			...scopeEvidence,
			envelope,
			stale: false,
			value: servedValue(envelope)
		}
	};
	if (validity === "stale" && envelope.kind !== "neg") return {
		checkedThrough,
		hit: {
			...scopeEvidence,
			envelope,
			stale: true,
			value: servedValue(envelope)
		}
	};
	if (envelope.kind === "neg") return { checkedThrough };
	return {
		checkedThrough,
		staleCandidate: {
			...scopeEvidence,
			envelope,
			stale: true,
			value: servedValue(envelope)
		}
	};
}
/**
* DEVELOPMENT-TIME TEACHING TOOL — NOT A PRODUCTION SAFETY NET. It is dev-gated by design
* (`config.dev === true`), and it stays that way. Two readings to head off, in both directions:
*
*   - DO NOT PROMOTE IT TO PRODUCTION believing it closes a privacy hole. It does not close one:
*     an application that hand-mixes per-principal content into a public-scoped fill without
*     touching `ctx.request` at all is untouched by it, so it was never a boundary — it is a
*     tripwire that catches the honest mistake early, in the one environment where the developer
*     is watching. Making the demotion live in production would make an entry's STORAGE TIER a
*     function of what a factory happened to read on one execution: the same key lands durable on
*     the request whose factory took the cache-hit path and L1-only on the request whose factory
*     read the principal, so the tier stops being a property of the call and becomes a property of
*     the run. That is nondeterministic caching, and it is a worse bargain than the mistake it
*     would be trying to catch.
*
*   - DO NOT DELETE IT believing it is dead code. INV-4 pins it, and the demotion is exactly
*     what that invariant's enforceable half asserts — a public fill that read request state is
*     confined to L1 and never reaches the shared tier. Blind it and the sacred suite reds.
*
* It deliberately does NOT feed the shared-cacheability gate. That gate answers a different
* question — whether the scopes the library DECLARED and STORED permit a shared HTTP cache to keep
* the response — and it answers it from evidence that survives the request. This guard sees
* only what one factory did while it ran. Wiring a runtime observation into that gate would make
* the emitted response posture vary run to run for the same route, which is the same nondeterminism
* as above, on the wire instead of in the tiers.
*/
function buildRequestGuard(config, options) {
	const { request } = options;
	if (!(config.dev === true && options.scope === "public") || request === void 0) return {
		request,
		wasTouched: () => false
	};
	let touched = false;
	return {
		request: new Proxy(request, { get(target, prop, receiver) {
			touched = true;
			return Reflect.get(target, prop, receiver);
		} }),
		wasTouched: () => touched
	};
}
/** Acquire the cross-isolate lock if requested, returning its fence handle. */
async function maybeLock(config, options) {
	if (options.lock !== true) return;
	const { lock } = config;
	if (!lock) return;
	return await lock.acquire(options.key);
}
/**
* Classify a rejected L2 write-back into the kernel's write-failure semantics.
* WIRES CLASSIFICATION + TELEMETRY ONLY — no REFRESH_QUEUE / retry machinery is built
* (that is deferred). A recognized transient failure is SUPPRESSED: the write did not
* become durable, but the operation does not fail the caller and produces no
* fail-closed/unknown state — the function returns `false` (non-durable) and emits the
* reserved telemetry name. Everything else keeps today's behavior and PROPAGATES:
*   - `throttled` (retryable) → `writeback_throttled`, suppressed (`durable:false`).
*   - `unavailable` (retryable) → `l2_write_error`, suppressed (`durable:false`).
*   - `too_large` (permanent, non-retryable) → rethrown, never silently suppressed.
*   - unclassified → surfaced as `l2_write_error` telemetry, then rethrown unchanged.
*/
function classifyWriteBackFailure(config, key, error) {
	const classified = isStoreWriteError(error) ? error : void 0;
	if (classified?.code === "throttled") {
		emitTelemetry(config, {
			key,
			type: "writeback_throttled"
		});
		return false;
	}
	if (classified?.code === "unavailable") {
		emitTelemetry(config, {
			key,
			type: "l2_write_error"
		});
		return false;
	}
	if (classified === void 0) emitTelemetry(config, {
		key,
		type: "l2_write_error"
	});
	throw error;
}
/**
* Is this stored envelope a `val` that protects? THE suppression-worthiness
* test, and it lives in ONE place because two seats have to agree about it:
*
*   - the SERVE seat (`gracedStillServable`) asks it of the candidate THIS read holds, deciding
*     whether an error-path serve may draw on it;
*   - the WRITE seat (`negDisplacesProtectedVal`) asks it of the envelope the born-monotone compare
*     already fetched, deciding whether a `kind:'neg'` candidate may land on it.
*
* One function, because the two answers may never disagree in the direction that destroys data: a
* val the serve path would hand to a caller is exactly a val the write path must refuse to bury.
* Written twice they drift, and the drift is silent — each site keeps passing its own tests while
* the pair stops meaning one thing.
*
* `validity` is the verdict, produced by the same machinery on both sides. `unknown` is
* deliberately NOT settled here: it is a GAP rather than a window, and the two seats answer a gap
* differently on purpose — the serve seat refuses to serve THROUGH one, the write seat refuses to
* write through one. Each states that rule, with its own derivation, at its own site.
*/
function e7ProtectsStoredVal(envelope, validity) {
	return envelope.kind !== "neg" && (validity === "fresh" || validity === "stale");
}
/** The `UnknownResolution` a RE-VALIDATION of an already-stored envelope asks for: settle
* an unknown tag with one live registry read. Named once because the two seats that re-validate the
* same envelope — the serve seat's grace check and the write seat's neg-write guard — must ask the SAME
* question of it; drifting onto different resolutions would let them reach different verdicts about
* one entry and call it agreement. Distinct from `unknownResolution`, which reports the instance's
* configured READ-time policy. */
var REVALIDATION_RESOLUTION = "registry-check";
/**
* the kind-aware guard: would landing a `kind:'neg'` candidate here bury a
* `val` the letter protects? Asked of the envelope the born-monotone compare ALREADY fetched, which
* is why it lives at the write layer — one boundary, so every neg-write source passes it identically
* and the outcome cannot vary by the route that reached it.
*
* The question is ONTOLOGICAL: what does the STORE hold? That is a different question from the one
* the filling caller's own read can answer, and the gap between them is the whole defect this closes
* — a caller blind to a servable val (a tier that was unreadable for a moment, or a join whose
* leader read nothing) would otherwise write straight over it, and every later reader would be
* served the negative. Note also which KIND is which: `FilledAttempt.storedKind` describes what THIS
* fill produced, while the kind read here is the STORED envelope's — different objects, and only the
* second one can answer this.
*
* The within-grace term is not re-invented: it is the verdict `validate` gives, asked on the
* same terms the serve seat asks for it (`gracedStillServable`), and settled by the shared predicate
* both seats share.
*/
async function negDisplacesProtectedVal(config, key, existing, inval, consistency) {
	const stored = readerEnvelope(config, codecOf(config), key, existing);
	if (!stored) return false;
	if (!inval) return e7ProtectsStoredVal(stored, "fresh");
	let validity;
	try {
		({validity} = await inval.validate(stored, consistency, REVALIDATION_RESOLUTION, config.defaults?.onUnavailable));
	} catch {
		validity = "unknown";
	}
	return e7ProtectsStoredVal(stored, validity === "unknown" ? "stale" : validity);
}
/**
* Born-monotone write-back (invariant 17): never replace a stored
* envelope with a strictly-higher `(bornEpoch, fence)`. Read-compare-write against
* L2 — the KV CAS-less residual race is documented and bounded to one refresh cycle.
*`bornMs` is never an arbiter. `durable` is `true` when the candidate landed (or a
* strictly-newer envelope already sits durably), `false` when a classified `throttled`/`unavailable`
* failure suppressed it.
*
* It is ALSO where the kind-aware guard sits, for the reason the guard needs and this function
* already has: the stored envelope is in hand here. A `kind:'neg'` candidate may only land on a true
* miss, so every neg-write source — a solo caller's own disposition, a blind leader's in-window
* recording — meets the same test on the same evidence, and route-independence holds by construction
* rather than by matching two call sites. `val`-over-`neg` is untouched: recovery is the direction
* The neg-write guard leaves open, and the guard is asked only of a neg candidate.
*/
async function bornMonotoneWrite(config, l2, key, encoded, candidate, inval, consistency) {
	let existing;
	try {
		existing = await l2.get(key);
	} catch (error) {
		return {
			durable: classifyWriteBackFailure(config, key, error),
			writeSuppressed: false
		};
	}
	if (existing) {
		if (candidate.kind === "neg" && await negDisplacesProtectedVal(config, key, existing, inval, consistency)) {
			emitTelemetry(config, {
				key,
				type: TELEMETRY_EVENTS.neg_suppressed
			});
			return {
				durable: false,
				writeSuppressed: true
			};
		}
		const meta = existing.metadata ?? {};
		const storedBornEpoch = typeof meta.bornEpoch === "number" ? meta.bornEpoch : 0;
		const storedFence = typeof meta.fence === "number" ? meta.fence : -1;
		const candidateFence = candidate.fence ?? -1;
		if (storedBornEpoch > candidate.bornEpoch || storedBornEpoch === candidate.bornEpoch && storedFence > candidateFence) return {
			durable: true,
			writeSuppressed: false
		};
	}
	try {
		await l2.set(key, encoded, { metadata: {
			bornEpoch: candidate.bornEpoch,
			bornMs: candidate.bornMs,
			codecId: candidate.codecId,
			enc: candidate.enc,
			kind: candidate.kind,
			scope: candidate.scope,
			tags: candidate.tags,
			v: candidate.v,
			...candidate.fence === void 0 ? {} : { fence: candidate.fence }
		} });
		return {
			durable: true,
			writeSuppressed: false
		};
	} catch (error) {
		return {
			durable: classifyWriteBackFailure(config, key, error),
			writeSuppressed: false
		};
	}
}
/**
* Re-validate a graced candidate's tags AT SERVE TIME: a hard delete
* that landed after the candidate was read (e.g. during the failed refresh) must
* fence it here — a graced value is never served once its purge is visible. No
* `Invalidation` (registry-absent) ⇒ nothing to invalidate it ⇒ always servable.
*
* AT `strong` THIS IS A LIVE REGISTRY CHECK LIKE ANY OTHER, so it honors `onUnavailable`
* exactly as its sibling sites do. Leaving it unguarded did not avoid a throw — it MANUFACTURED
* one: the guard inside `validate` fires for a caller who explicitly opted out, which is the one
* caller who asked not to see it.
*
* `factoryError` is what this re-validation runs on behalf of, and it rides out as the thrown
* error's `cause`. That answers the reason the guard was omitted here — the fear that a throw would
* REPLACE the caller's factory error — without leaving the site unguarded: nothing is dropped, and
* callers still get the one type the API names.
*
* WHEN THE OPT-OUT IS TAKEN this call drops to what an eventual read would do, and the candidate is
* settled against local knowledge alone. A tag whose hardness cannot be established stays UNKNOWN,
* and an unknown tag is never graced (ladder sends unknown to 5a and never to grace) —
* when hardness is exactly what we failed to learn, a hard purge is one of the things the gap could
* be concealing. So an unknown candidate is not servable, and the factory's own error is what the
* caller ends up with.
*/
async function gracedStillServable(inval, consistency, envelope, onUnavailable, factoryError) {
	if (!inval) return e7ProtectsStoredVal(envelope, "fresh");
	let validity;
	try {
		({validity} = await inval.validate(envelope, consistency, REVALIDATION_RESOLUTION, onUnavailable));
	} catch (error) {
		if (error instanceof RegistryUnavailableError) throw new RegistryUnavailableError(error.message, { cause: factoryError });
		throw error;
	}
	return e7ProtectsStoredVal(envelope, validity);
}
/**
* Write a `kind:'neg'` envelope for a `not_found` fact.
* `notFoundTtl` is the per-call opt-in; the write shares the same born-monotone
* fence as any write-back.
*
* Returns the write's OUTCOME (durability, and whether the guard refused it) AND
* the `bornMs` it stamped — the neg's birth instant, read off the clock exactly once here and
* carried out for every later consumer, the same way a value fill's is.
*
* `inval`/`consistency` ride down for the guard alone: it decides against the STORE, and deciding
* against the store means asking the same validity question a reader asks of what it finds there.
*/
async function writeNegEnvelope(config, key, tags, scope, bornEpoch, inval, consistency) {
	const { l2 } = config;
	const bornMs = config.clock.now();
	if (!l2) return {
		bornMs,
		durable: true,
		writeSuppressed: false
	};
	const codec = codecOf(config);
	const encoded = codec.encode(null);
	return {
		bornMs,
		...await bornMonotoneWrite(config, l2, key, encoded, {
			bornEpoch,
			bornMs,
			enc: "json",
			grace: 0,
			key,
			kind: "neg",
			scope,
			size: encoded.length,
			src: "factory",
			storedAt: bornMs,
			tags,
			ttl: 0,
			v: 3,
			val: null,
			codecId: codec.id
		}, inval, consistency)
	};
}
/** Build the INV-26 neg-suppression serve of a still-servable candidate (a 404 that a servable
* `val` displaces). ONE constructor for both call sites — the leader-sighted classification and a
* blind leader's per-waiter tail — so `value` is always the served candidate's own value.
*
* It also EMITS, and the emit belongs here rather than at either caller. The design lists `neg_suppressed`
* among a suppression's consequences, and the design attaches those consequences to whichever seat renders
* the suppression, one event per suppression. This is the serve seat's renderer; the write layer's
* guard emits its own at its own site. Emitting from the shared constructor is what makes one
* event per suppression structural instead of a convention two call sites have to remember —
* before it, the epistemic suppression that the own rationale describes fired no telemetry at all,
* so the commonest case was the silent one. */
function negSuppressed(config, key, served) {
	emitTelemetry(config, {
		key,
		type: TELEMETRY_EVENTS.neg_suppressed
	});
	return {
		kind: "neg-suppressed",
		served,
		value: served.hit.value
	};
}
/**
* Write the `not_found` neg and package it as the `filled` attempt every
* SERVED-fill path already understands: `value:undefined`, the neg's stored tag set, and the write's
* own `durable`/`bornMs`. A neg carries no `bornEpoch` on the outcome — its presence is the marker
* of a successful VALUE fill, which this is not — so L1 hydration falls back to the caller's
* outer floor exactly as the value path intends.
*
* ONE writer of the neg for two call sites: the leader that read a candidate of its own that
* re-validated DEAD (its own disposition, in `classifyFillError`), and any blind waiter's own tail
* (`runOwnNotFoundTail`). Both stamp the fill's tags/scope/bornEpoch, so a neg written on either path
* is byte-identical to the one a solo caller writes — and both meet the write-layer guard, which is
* why a SUPPRESSED write is one of the outcomes this can carry (`writeSuppressed`): the fill still
* produced the not-found FACT and still serves it, but nothing was stored and nothing is evicted.
*/
async function negWriteFilled(config, key, tags, scope, bornEpoch, inval, consistency) {
	const neg = await writeNegEnvelope(config, key, tags, scope, bornEpoch, inval, consistency);
	return {
		bornMs: neg.bornMs,
		durable: neg.durable,
		kind: "filled",
		storedKind: "neg",
		tags,
		value: void 0,
		writeSuppressed: neg.writeSuppressed
	};
}
/**
* The factory error-path classification, extracted out of
* `fillAndStore` to keep its cognitive complexity down:
*   - `ctx.skip()` (M10/invariant 24): `{kind:'skipped'}` — nothing stored,
*     nothing served; no write-back of any kind, not even a neg.
*   - `not_found` (404, a fact — never masked): with `notFoundTtl` declared and THIS fill's own read
*     holding a still-servable `val`, suppress the neg-write and serve that val — a neg must never
* displace a servable value — with no `servedOnError` (a suppressed neg is not an
*     error-path serve). This fill's own copy re-validated DEAD ⇒ write the neg and serve it (its own
*     disposition). This fill read NO copy at all (a blind leader) ⇒ ride the 404 out as a shareable
*     `not-found` FACT so each waiter runs its own tail against its own read ( `NotFoundFact
*     Attempt`), the mirror of the retriable branch's fenced-vs-fact split just below. Without
*     `notFoundTtl`, rethrow.
*   - retriable (`staleIfError`, default `isRetriableHttp`) with a graced `val`
* still servable at serve time: serve it, `servedOnError: true`. A
*     `kind:'neg'` graced candidate is NEVER offered here — it is never grace/SIE-
* served — so a neg-only history rethrows.
*   - otherwise (non-retriable, or no servable graced copy): rethrow — a failed or
* timed-out factory never modifies the stored good value.
*/
async function classifyFillError(error, config, key, tags, scope, bornEpoch, inval, consistency, options, graced) {
	if (error instanceof SkipSignalError) return { kind: "skipped" };
	if (isNotFound(error)) {
		if (options.notFoundTtl === void 0) throw error;
		if (graced) {
			if (await gracedStillServable(inval, consistency, graced.hit.envelope, config.defaults?.onUnavailable, error)) return negSuppressed(config, key, graced);
			return await negWriteFilled(config, key, tags, scope, bornEpoch, inval, consistency);
		}
		return {
			error,
			kind: "not-found",
			neg: await negWriteFilled(config, key, tags, scope, bornEpoch, inval, consistency),
			scope
		};
	}
	if (options.grace !== void 0 && staleIfErrorOf(config)(error)) {
		if (graced) {
			if (await gracedStillServable(inval, consistency, graced.hit.envelope, config.defaults?.onUnavailable, error)) return {
				kind: "graced",
				served: graced,
				value: graced.hit.value
			};
			return {
				error,
				kind: "fenced"
			};
		}
		return {
			error,
			kind: "retriable-failed"
		};
	}
	throw error;
}
/**
* Fill: run the factory, fence the write-back, then born-monotone
* write-through to L2. Returns `{kind:'filled', value}` on success,
* `{kind:'fenced'}` when the fill was fenced by a hard purge visible at write-back
* (never store or serve a fenced value), `{kind:'skipped'}` on
* `ctx.skip()` (M10/invariant 24), or `{kind:'graced', value}` when the
* factory threw a retriable error and `graced` — the currently-known
* stale-but-servable `val` — is STILL servable once re-validated at serve time.
*`bornEpoch` is captured by the caller at FILL START so a
* purge that bumps the epoch after the fill began cannot retroactively make the
* born value look fresh. Error-path handling is `classifyFillError` above.
*/
async function fillAndStore(config, options, key, inval, consistency, bornEpoch, graced, ambient) {
	const codec = codecOf(config);
	const { l2 } = config;
	if (!l2) return notImplemented("cache.getOrSet (no l2 configured)", config, options);
	const declaredUserTags = sortedUserTags(options.tags);
	const declaredTags = envelopeTags(declaredUserTags, key, config.namespace);
	const accum = makeTagAccumulator(declaredUserTags);
	const resolvedScope = resolveScope(options);
	const scope = resolvedScope.literal;
	const guard = buildRequestGuard(config, options);
	const lockHandle = await maybeLock(config, options);
	const ctx = {
		dependsOn(tag, dependsOptions) {
			if (dependsOptions?.l3 === false) notImplemented("FactoryCtx.dependsOn({ l3: false }) — deferred to v0.5", config, options);
			if (accum.isSettled()) throw new FactorySettledError(key);
			accum.addMembership(tag);
			recordDependency(ambient, {
				scope: fillScopeEvidence(scope),
				tags: [tag],
				...dependsOptions?.l3 === void 0 ? {} : { l3: dependsOptions.l3 }
			});
		},
		fail(err) {
			if (err === void 0) throw new Error("factory failed (non-retriable)");
			if (err instanceof Error) throw err;
			throw new Error("factory failed (non-retriable)", { cause: err });
		},
		/** Deferred to v0.4 — explicit grace-reuse from within a fill is not part of this phase. */
		reuseGraced() {
			return notImplemented("FactoryCtx.reuseGraced (deferred to v0.4)", config, options);
		},
		...guard.request === void 0 ? {} : { request: guard.request },
		setTags(tags) {
			if (accum.isSettled()) throw new FactorySettledError(key);
			accum.author(tags);
		},
		/** Deferred to v0.4 — the TTL-override (what it overrides, how it meets grace and
		* the duration grammar) is a v0.4 design slot, not a silent no-op (spec-surface sweep). */
		setTtl() {
			notImplemented("FactoryCtx.setTtl (deferred to v0.4)", config, options);
		},
		signal: new AbortController().signal,
		skip() {
			throw new SkipSignalError();
		}
	};
	try {
		let value;
		try {
			value = await options.factory(ctx);
		} catch (error) {
			accum.markSettled();
			return await classifyFillError(error, config, key, declaredTags, scope, bornEpoch, inval, consistency, options, graced);
		}
		accum.markSettled();
		const userTags = accum.settleUserTags();
		validateUserTags(userTags);
		const tags = envelopeTags(userTags, key, config.namespace);
		if (inval && tags.length > 0 && !inval.fenceWriteBack(tags, bornEpoch)) return { kind: "fenced" };
		const bornMs = config.clock.now();
		const encoded = codec.encode(value);
		const fence = lockHandle?.fence;
		const envelope = {
			bornEpoch,
			bornMs,
			enc: "json",
			grace: 0,
			key,
			kind: "val",
			scope,
			size: encoded.length,
			src: "factory",
			storedAt: bornMs,
			tags,
			ttl: 0,
			v: 3,
			val: value,
			codecId: codec.id,
			...fence === void 0 ? {} : { fence }
		};
		const written = resolvedScope.l1Only || guard.wasTouched() ? {
			durable: false,
			writeSuppressed: false
		} : await bornMonotoneWrite(config, l2, key, encoded, envelope, inval, consistency);
		return {
			bornEpoch,
			bornMs,
			durable: written.durable,
			kind: "filled",
			storedKind: "val",
			tags,
			value,
			writeSuppressed: written.writeSuppressed
		};
	} finally {
		await lockHandle?.release();
	}
}
/** Write-back attempt budget: fence-taught retries. A fill fenced by a
* hard purge that landed mid-fill re-mints its `bornEpoch` from the now-advanced verified
* high-water and refetches, up to this many attempts before failing typed. */
var WRITE_BACK_ATTEMPT_BUDGET = 3;
/**
* `fillAndStore` fronted by the CHECK-THEN-FETCH strong-miss reorder
* and the fence-taught retry. On a
* `strong`, registry-backed fill the live un-memoized registry check
* (`inval.liveCheckForFill`) runs over the fill's tags BEFORE the factory; the fill's
* `bornEpoch` is raised to the now-current verified high-water, so the born value
* postdates every purge that check observed — hard OR soft. A strong read
* therefore never serves a stale-born or hard-dead value, and two strong reads of an
* unmutated key agree.
*
* If the LOCAL write-back fence (`fenceWriteBack`) fences the fill — a hard purge
* landed DURING the factory and was delivered/verified into local knowledge — the fill
* RE-MINTS `bornEpoch` from the now-advanced verified high-water (`localBornEpoch`,
* which a resync or this call's own live check has caught up between attempts, per
* `#recordChecked`) and REFETCHES: the honest post-acceptance fetch the purge demanded,
* which borns past it and passes the fence ( observations commission later
* fetches, they never bless the earlier one). Bounded by the attempt budget; an
* attempt whose re-mint cannot advance the floor gives up (the fence is not from an
* observable purge). Exhaustion returns `fenced` — `getOrSet` throws `FencedError`,
* `getOrSetEntry` misses. `eventual`/registry-absent/tags-empty fills skip
* the pre-fetch check but share the retry.
*/
async function fillAndStoreChecked(config, options, key, inval, consistency, bornEpoch, graced, ambient) {
	const tags = inval ? envelopeTags(sortedUserTags(options.tags), key, config.namespace) : [];
	let bornEpochForFill = bornEpoch;
	if (consistency === "strong" && inval && tags.length > 0) {
		const checkedThrough = await inval.liveCheckForFill(tags, config.defaults?.onUnavailable);
		bornEpochForFill = Math.max(bornEpochForFill, checkedThrough);
	}
	for (let attempt = 0; attempt < WRITE_BACK_ATTEMPT_BUDGET; attempt++) {
		const result = await fillAndStore(config, options, key, inval, consistency, bornEpochForFill, graced, ambient);
		if (result.kind !== "fenced" || !inval || tags.length === 0) return result;
		const reMinted = Math.max(bornEpochForFill, inval.localBornEpoch());
		if (reMinted === bornEpochForFill) return result;
		bornEpochForFill = reMinted;
	}
	return { kind: "fenced" };
}
/** A fresh, empty singleflight registry for one isolate. */
function newSingleflight() {
	return {
		byIdentity: /* @__PURE__ */ new Map(),
		byKey: /* @__PURE__ */ new Map()
	};
}
/**
* The singleflight JOIN IDENTITY ( strict option-compatibility, step
* the canonical key plus every structurally-relevant option a
* waiter would otherwise silently miss. Two concurrent calls coalesce onto ONE
* factory invocation iff their join identities are byte-identical strings.
*
*   - `key`: the already-canonicalized (ns-versioned, scoped) key — two calls
*     for different keys must never coalesce.
*   - `consistency`: a `strong` request never joins a non-strong (`eventual`)
*     fill — an eventual waiter MAY join a strong fill (strictly safer), so the
*     identity is keyed on the REQUESTING call's own consistency, not some
*     merged value; a `strong` caller's identity differs from an `eventual`
*     caller's, so they never hash equal and each strong call always runs its
*     own fill, while two `eventual` calls (or two `strong` calls) still share.
*   - `apiForm`: `getOrSet` vs `getOrSetEntry` — a plain-`T` waiter must
*     never join an entry-form fill's ctx (skip-capability differs, M10).
*   - structural options (GPT note): tags (sorted+deduped, so order never
*     matters), ttl, grace, scope, notFoundTtl, codec id — a mismatch here means
*     a waiter could receive an envelope missing its own declared dependency
*     tags, the exact stringly-typed no-op class this library exists to kill.
*
* `dev` mismatch handling (throw vs a separate fill + telemetry event) belongs
* to a later cluster; this cluster only builds the identity and coalesces
* exact matches — a mismatch here simply produces a different map key, so it
* already runs its own separate fill (the safe default) without yet emitting
* `singleflight_option_mismatch` or dev-throwing.
*/
function joinIdentity(key, consistency, apiForm, codec, options) {
	const userTags = sortedUserTags(options.tags);
	return JSON.stringify({
		apiForm,
		codecId: codec.id,
		consistency,
		grace: options.grace ?? null,
		key,
		notFoundTtl: options.notFoundTtl ?? null,
		scope: scopeLiteral(options),
		tags: userTags,
		ttl: options.ttl ?? null
	});
}
/** A structural options mismatch between concurrent same-key fills.
* The message points at the fix (declare compatible options); dev throws it. */
var SingleflightOptionMismatchError = class extends Error {
	constructor(userKey) {
		super(`singleflight option mismatch for key ${JSON.stringify(userKey)}: concurrent calls declared incompatible options (tags/ttl/grace/scope/notFoundTtl/codec). Make the options identical, or serialize the calls.`);
		this.name = "SingleflightOptionMismatchError";
	}
};
/**
* Detect + handle a singleflight OPTION MISMATCH: another fill for
* the SAME canonical key is already in flight under a DIFFERENT join identity.
* `dev:true` throws (a caller bug surfaced loudly); prod runs a SEPARATE fill and
* emits `singleflight_option_mismatch` — carrying the colliding key + the
* declared tags (hosted sinks salt every field, INV-15). A matching-identity
* joiner never reaches here (it joined in `singleflight` before this ran).
*/
function handleOptionMismatch(map, key, identity, config, options) {
	const identities = map.byKey.get(key);
	if (identities === void 0) return;
	let incumbent;
	for (const [inFlight, record] of identities) if (inFlight !== identity) {
		incumbent = record;
		break;
	}
	if (incumbent === void 0) return;
	if (config.dev === true) throw new SingleflightOptionMismatchError(options.key);
	emitTelemetry(config, {
		key,
		tags: incumbent.envelopeTags,
		type: "singleflight_option_mismatch"
	});
}
/** Track this identity as in flight for its canonical key (mismatch detection),
* remembering its full stored tag set for a later mismatch event's tag ref. */
function trackKeyIdentity(map, key, identity, fillEnvelopeTags) {
	const identities = map.byKey.get(key) ?? /* @__PURE__ */ new Map();
	const existing = identities.get(identity);
	if (existing) existing.count += 1;
	else identities.set(identity, {
		count: 1,
		envelopeTags: fillEnvelopeTags
	});
	map.byKey.set(key, identities);
}
/** Untrack this identity once its fill settles (mismatch detection cleanup). */
function untrackKeyIdentity(map, key, identity) {
	const identities = map.byKey.get(key);
	if (identities === void 0) return;
	const record = identities.get(identity);
	if (record !== void 0) {
		record.count -= 1;
		if (record.count <= 0) identities.delete(identity);
	}
	if (identities.size === 0) map.byKey.delete(key);
}
/**
* Run `fill` under in-isolate singleflight (invariant 5): if a fill with
* the SAME join identity is already in flight, join it instead of starting a
* second factory invocation — both callers resolve to the one result. Otherwise,
* a concurrent fill for the same KEY under a DIFFERENT identity is an OPTION
* MISMATCH: dev throws, prod runs a separate fill + emits the event
* (`handleOptionMismatch`). Then register this call's fill and run it.
*
* CRITICAL — coalescing under `Promise.all` / same-tick concurrent callers
* (not just `fc.scheduler` interleavings): the in-flight promise is registered
* into the map SYNCHRONOUSLY, in the same microtask turn as the identity
* lookup, with NO `await` between the `byIdentity.get` check and the
* registration. `fill()` itself is only invoked (and starts doing real async
* work) after registration. Two callers racing through `Promise.all` each
* invoke `singleflight` synchronously up to this point; the first to run
* (JS's single-threaded execution order gives the two `Promise.all` callbacks
* a well-defined sequential dispatch — there is no `await` before either call
* reaches here) wins the identity check and registers, so a same-identity
* second call synchronously observes the first's entry and joins it, while a
* different-identity second call synchronously observes the mismatch. The entry
* is removed in a `finally` once the fill settles (success or failure) — a
* subsequent, non-concurrent call always runs its own fresh fill.
*/
async function singleflight(map, key, identity, config, options, fill) {
	const existing = map.byIdentity.get(identity);
	if (existing) return await existing;
	handleOptionMismatch(map, key, identity, config, options);
	trackKeyIdentity(map, key, identity, envelopeTags(sortedUserTags(options.tags), key, config.namespace));
	const registration = (async () => {
		try {
			return await fill();
		} finally {
			map.byIdentity.delete(identity);
			untrackKeyIdentity(map, key, identity);
		}
	})();
	map.byIdentity.set(identity, registration);
	return await registration;
}
/**
* Open the mint for one read, snapshotting the `bornEpoch` floor SYNCHRONOUSLY — the writer's
* applied registry epoch at fill start. Taking it at OPEN is the fence-critical part:
* a purge that lands via a concurrent bus frame after this call cannot lift the floor, so a fill
* that BEGAN before a concurrent purge still fences correctly at write-back (INV-9/INV-12).
* Registry-absent ⇒ 0.
*/
function openBornMint(inval) {
	const floor = inval ? inval.localBornEpoch() : 0;
	return { stamp: (checkedThrough) => Math.max(floor, checkedThrough) };
}
/**
* What a SERVE can prove about the served value's durability. A serve
* reports only what ITS OWN evidence supports:
*
*   - found in L2 ⇒ `true`. The read itself observed the value in the shared tier, so the claim is
*     backed by something this turn actually did.
*   - the STORED scope resolves L1-only (a `usr:` principal) ⇒ `false`. Derived from the identity
*     the entry was stored under, not from this turn: such an entry is never published to L2, so
*     the kernel KNOWS it is not durable.
*   - otherwise ⇒ ABSENT. A `pub`/`ten:` entry found in L1, or a scope the surface did not return
* readably: the L1 envelope carries no write-back record, and the serve algorithm does
*     not consult L2 on an L1 hit, and adding a read to populate this field would make an L1 hit
*     cost what the driver contract forbids. The write-through outcome simply cannot be settled here, and
*     an unsettled question is reported as such rather than guessed in either direction.
*
* Returned as a spread FRAGMENT so absence is STRUCTURAL — the property simply not present, never
* a sentinel and never an explicit `undefined` (`exactOptionalPropertyTypes` keeps the difference
* observable to a consumer that compares against `false`).
*/
function serveDurability(tier, hit) {
	if (tier === "l2") return { durable: true };
	const evidence = hitScopeEvidence(hit);
	if (evidence.kind === "readable" && storedScopeIsL1Only(evidence.literal)) return { durable: false };
	return {};
}
/**
* The error-path grace serve: a stored candidate that re-validated as
* still-servable is served `stale:true` after the fill failed. `graced` (a retriable failure)
* carries `servedOnError`; `neg-suppressed` (a 404, which is not `staleIfError`) does not.
*
* Every field here reports the CANDIDATE — the entry actually being served — rather than the fill
* that failed: its tier is the tier it was read from, its durability what that tier can prove,
*its age its own stamp. The earlier shape named `tier:'l2'` and `durable:true` as
* constants, for a value that may have been found in L1 and never written to a shared tier at all.
*/
function gracedServe(attempt, ambient) {
	const { served } = attempt;
	recordServedHit(ambient, served.hit);
	return {
		...serveDurability(served.tier, served.hit),
		bornMs: served.hit.envelope.bornMs,
		skipped: false,
		stale: true,
		tier: served.tier,
		value: attempt.value,
		...attempt.kind === "graced" ? { servedOnError: true } : {}
	};
}
/**
* One caller's error-path tail over a SHARED retriable failure: the fill that
* failed produced no serve of its own (a blind leader), so it handed out the classified error as a
* FACT. This runs on behalf of a caller against ITS OWN read — a singleflight joiner that read a
* copy of its own, or the blind leader itself. Each state resolves to exactly what a SOLO caller in
* that same state resolves to, because a caller's outcome must not depend on which caller happened
* to win singleflight:
*
*   - this caller read a `val` candidate that STILL re-validates as servable now ( a
*     hard delete during the fill must not resurrect it): serve it `{stale:true, servedOnError}`
* with THIS candidate's own evidence (tier/durable/age), exactly as a leader's own
*     graced serve would. `staleCandidate` is already a `ServedCandidate`, so the evidence-riding
*     machinery is reused unchanged.
*   - this caller read a candidate that re-validates DEAD: it REFUSES the serve and takes the
*     classified failure — returned as the FENCED OUTCOME, not thrown, so the wrapper applies the
*     same per-form mapping the solo path gets for this state (the plain form rethrows the typed
*     error, the entry form resolves a miss). Throwing here would make an entry-form joiner reject
*     where an identical solo call resolves a miss.
*   - this caller read no candidate at all: the classified error is rethrown, exactly as a solo
* caller with nothing to fall back on rethrows it.
*
* A joiner's recourse is thus judged against its OWN read, never the leader's disposition — the
* leader's blindness is a fact it shares, not a verdict it imposes.
*/
async function runOwnErrorTail(config, options, inval, consistency, staleCandidate, error, ambient) {
	if (options.grace !== void 0 && staleCandidate !== void 0) {
		if (await gracedStillServable(inval, consistency, staleCandidate.hit.envelope, config.defaults?.onUnavailable, error)) return gracedServe({
			kind: "graced",
			served: staleCandidate,
			value: staleCandidate.hit.value
		}, ambient);
		return {
			durable: false,
			error,
			skipped: false,
			stale: false,
			tier: "miss"
		};
	}
	throw error;
}
/** The read tiers in order: L1 then L2. Absent tiers are skipped. */
function tiersOf(config) {
	const tiers = [];
	if (config.l1) tiers.push([config.l1, "l1"]);
	if (config.l2) tiers.push([config.l2, "l2"]);
	return tiers;
}
/** Write the freshly-filled value into L1 (write-through) — best-effort. The whole fill
* outcome rides in, because every field written here belongs to that ONE fill: its `bornEpoch` so
* both tiers validate identically ( `fallbackBornEpoch` covers the neg-write outcome, which
* stamps none), its `bornMs` so both tiers report the same age ( birth is an event, and a value
* has ONE: the stamp is minted at fill and carried here, never re-read off the clock, which would
* date the L1 copy to the moment the L2 write finished and make one value two ages), and its STORED
* tag set (settle-union) so the two tiers are tag-coherent: a later L1 hit replays the
* same purge handles L2 carries.
*
* A `kind:'neg'` fill whose write LANDED stores nothing here and EVICTS whatever L1 still holds for
* the key — two halves of one rule, and neither is sufficient alone. A neg whose write the guard
* SUPPRESSED does neither: eviction follows the write's fate (see below).
*
* It stores nothing because the neg's storage is `writeNegEnvelope`'s single L2 write
*  — that function is the neg's whole write path, and it deliberately writes one tier. What
* this write-through would put in L1 is a `kind:'val'`-stamped copy of the neg's placeholder body:
* an envelope claiming to be a VALUE, holding a value no origin produced. Read back, it would
* surface that placeholder (the read path's neg rule keys on the stored `kind`, which this write
* misstates) and, worse, stand as a grace/stale-if-error candidate — precisely what invariant 7 forbids a
* neg from ever being. Today the default codec cannot encode `undefined` to a decodable body, so
* such a write lands as an unreadable entry that always misses; that is an accident of one codec,
* not a guarantee any other owes us.
*
* It evicts because L1 may ALREADY hold an OLDER value for this key. A fill runs when the read
* found nothing servable — and a tier read that a classified transient failure suppressed is
* a plain MISS over an entry that is still sitting there. Refusing the write alone leaves that
* entry standing, and it outlives the 404: once the tier reads cleanly again L1 is consulted first
* (steps 2/4) and answers with the superseded value, in preference to the neg L2 now holds —
* the origin's not-found reverted by a tier that was merely unreadable for a moment. Deleting ends
* the turn with L1 holding nothing for the key, so reads fall through to the tier that remembers
* the 404 and a served neg still reports the tier and durability its one write proves.
*
* Whether a neg SHOULD also LIVE in L1 is a storage-policy question the design does not settle — it would
* change that reported tier and durability — so this evicts rather than storing a neg envelope,
* closing the mislabel and the resurrection without deciding it.
*
* EVICTION FOLLOWS THE WRITE'S FATE. Both halves above are premised on a neg that
* LANDED: the eviction exists to stop a superseded L1 copy outliving a 404 the durable tier now
* remembers. A neg the guard suppressed leaves the durable tier remembering no 404 at all — the
* verdict the suppression rendered is that the val stands — so deleting that val's L1 copy would
* contradict the verdict on the way out, and cost a refill of a value that is still good. A
* suppressed write mutates nothing, in either tier. */
async function hydrateL1(config, codec, key, options, attempt, fallbackBornEpoch) {
	const { l1 } = config;
	if (!l1) return;
	if (attempt.storedKind === "neg") {
		if (attempt.writeSuppressed) return;
		try {
			await l1.delete(key);
		} catch {}
		return;
	}
	const { bornMs, tags, value } = attempt;
	const bornEpoch = attempt.bornEpoch ?? fallbackBornEpoch;
	try {
		await l1.set(key, codec.encode(value), { metadata: {
			bornEpoch,
			bornMs,
			codecId: codec.id,
			enc: "json",
			kind: "val",
			scope: scopeLiteral(options),
			tags,
			v: 3
		} });
	} catch {}
}
/**
* The successful foreground fill's serve: record the dependency, hydrate L1, and
* report the fill's own outcome.
*
* This is the one serve that can always answer `durable` (leaves it untouched): it performed
* the write-back itself and awaited the result, so it reports a real boolean rather than the absent
* arm the read-path serves need.
*/
async function filledServe(config, codec, key, options, scope, attempt, bornEpoch, ambient) {
	const storedTags = attempt.tags;
	recordDependency(ambient, {
		scope: fillScopeEvidence(scope),
		tags: storedTags
	});
	const filled = attempt.value;
	await hydrateL1(config, codec, key, options, attempt, bornEpoch);
	return {
		durable: attempt.durable,
		bornMs: attempt.bornMs,
		skipped: false,
		stale: false,
		tier: "origin",
		value: filled
	};
}
/**
* One caller's error-path neg tail over a SHARED `not_found` fact. The fill
* read no candidate of its own (a blind leader), so it recorded the fact and handed it out rather
* than deciding, from a read that saw nothing, what every waiter should be served. This decides
* DISPOSITION ONLY — it records nothing, because the recording already happened once,
* inside the fill's window. It runs on behalf of one caller against ITS OWN read (a joiner that read
* a copy of its own, or the blind leader itself), so that each caller resolves EXACTLY as a SOLO
* caller in that same state resolves — an outcome must not depend on which caller won singleflight:
*
*   - this caller read a `val` candidate that STILL re-validates as servable now: the guard serves
* that val `{stale:true}` with THIS candidate's own evidence (tier/durable/age), and — a
*     404 not being `staleIfError` — WITHOUT `servedOnError`, exactly as a solo sighted caller does.
*     The gate matches the solo path's: this arm is offered only when `grace` was declared (the solo
*     fill receives its `staleCandidate` only then).
*   - otherwise (this caller read no candidate, or its own copy re-validated dead): it takes the
*     RECORDED neg as its outcome, served `{tier:'origin'}` with that write's own evidence — the same
*     `filled` serve a solo blind caller performs over its own neg-write.
*
* A joiner's disposition is thus judged against its OWN read — its read is the existence proof the
* blind leader lacked — while the fact itself was recorded exactly once, by the fill that found it.
*/
async function runOwnNotFoundTail(config, options, inval, consistency, key, staleCandidate, fact, outerBornEpoch, ambient) {
	if (options.grace !== void 0 && staleCandidate !== void 0 && await gracedStillServable(inval, consistency, staleCandidate.hit.envelope, config.defaults?.onUnavailable, fact.error)) return gracedServe(negSuppressed(config, key, staleCandidate), ambient);
	return await filledServe(config, codecOf(config), key, options, fact.scope, fact.neg, outerBornEpoch, ambient);
}
/**
* A STALE hit → the SWR path (SWR). The OBSERVABLE contract, independent of `grace`:
* **this response always serves the stale value with `stale:true`** — never the
* refreshed/fresh result. A refresh runs synchronously (the full background /
* `waitUntil` / REFRESH_QUEUE machinery of step 6 stays deferred; a synchronous
* refresh is a placeholder that produces the same observable serve), and its result
* is written through for the NEXT read only. It never changes the value THIS call
* returns, and its failure never propagates to the caller — SWR's promise is a fast
* stale response.
*
* `servedOnError:true` iff the refresh failed RETRIABLY (per `staleIfError`)
* within a grace window (`grace` declared on the call) AND the stale copy is still
* servable after serve-time re-validation — the `graced` outcome. A
* successful refresh, a fenced/non-servable refresh, a suppressed 404 ( a neg
* never displaces a servable val, and a 404 is not `staleIfError`), and a
* non-retriable refresh failure all serve the stale value WITHOUT `servedOnError`.
* `grace` only affects whether the error path yields `servedOnError`, never whether
* the stale value is served (it always is).
*/
async function refreshOrGrace(config, options, key, inval, consistency, tier, hit, bornEpoch) {
	const codec = codecOf(config);
	let graced = false;
	try {
		const attempt = await fillAndStore(config, options, key, inval, consistency, bornEpoch, {
			hit,
			tier
		});
		if (attempt.kind === "filled") await hydrateL1(config, codec, key, options, attempt, bornEpoch);
		graced = attempt.kind === "graced" && options.grace !== void 0;
	} catch {}
	return {
		...serveDurability(tier, hit),
		bornMs: hit.envelope.bornMs,
		skipped: false,
		stale: true,
		tier,
		value: hit.value,
		...graced ? { servedOnError: true } : {}
	};
}
/**
* The core getOrSet path: L1 → L2 → foreground fill with the fence and
* Born-monotone write-back. Serves fresh directly; serves a soft-stale value
* with `stale:true`; refills on miss/dead/unknown. Shared by `getOrSet` (plain) and
* `getOrSetEntry`.
*/
async function runGetOrSet(config, options, inval, nsVersion, apiForm, inFlight, ambient) {
	const codec = codecOf(config);
	const scope = scopeLiteral(options);
	const key = canonicalKey(nsVersion.current, config.namespace, scope, options.key);
	const consistency = consistencyOf(config, options);
	const mint = openBornMint(inval);
	let checkedThrough = 0;
	let staleCandidate;
	for (const [store, tier] of tiersOf(config)) {
		const read = await readTier(store, codec, config, inval, consistency, key);
		checkedThrough = Math.max(checkedThrough, read.checkedThrough);
		if (read.hit) {
			if (read.hit.stale && consistency !== "strong") {
				recordServedHit(ambient, read.hit);
				return await refreshOrGrace(config, options, key, inval, consistency, tier, read.hit, mint.stamp(checkedThrough));
			}
			if (!read.hit.stale) {
				recordServedHit(ambient, read.hit);
				return {
					...serveDurability(tier, read.hit),
					bornMs: read.hit.envelope.bornMs,
					skipped: false,
					stale: false,
					tier,
					value: read.hit.value
				};
			}
		}
		const candidate = read.hit?.stale === true ? read.hit : read.staleCandidate;
		if (candidate !== void 0) staleCandidate = {
			hit: candidate,
			tier
		};
	}
	const bornEpoch = mint.stamp(checkedThrough);
	const attempt = await singleflight(inFlight, key, joinIdentity(key, consistency, apiForm, codec, options), config, options, async () => await fillAndStoreChecked(config, options, key, inval, consistency, bornEpoch, options.grace === void 0 ? void 0 : staleCandidate, ambient));
	if (attempt.kind === "filled") return await filledServe(config, codec, key, options, scope, attempt, bornEpoch, ambient);
	if (attempt.kind === "graced" || attempt.kind === "neg-suppressed") return gracedServe(attempt, ambient);
	if (attempt.kind === "retriable-failed") return await runOwnErrorTail(config, options, inval, consistency, staleCandidate, attempt.error, ambient);
	if (attempt.kind === "not-found") return await runOwnNotFoundTail(config, options, inval, consistency, key, staleCandidate, attempt, bornEpoch, ambient);
	if (attempt.kind === "skipped") return {
		durable: false,
		skipped: true,
		stale: false,
		tier: "miss"
	};
	return {
		durable: false,
		skipped: false,
		stale: false,
		tier: "miss",
		...attempt.error === void 0 ? {} : { error: attempt.error }
	};
}
/** Whether a raw regex-captured unit is a known `DurationUnit`. */
function isDurationUnit(unit) {
	return unit !== void 0 && Object.hasOwn(DURATION_UNIT_MS, unit);
}
/** Convert a `Duration` to milliseconds. The full `Duration` grammar is
* deferred; the retention lease and the reader's liveness heartbeat cadence need
* only a coarse ms value, so this parses the `<number><unit>` shape. */
function durationMs(duration) {
	const match = /^(?<value>[0-9]+(?:\.[0-9]+)?|\.[0-9]+)(?<unit>ms|s|m|h|d)$/u.exec(duration);
	const value = Number(match?.groups?.value ?? "0");
	const rawUnit = match?.groups?.unit;
	return value * (isDurationUnit(rawUnit) ? DURATION_UNIT_MS[rawUnit] : 1);
}
/** Build the per-instance invalidation core when a registry + bus are wired.
*Registry-absent instances (codec.test.ts) get `undefined` and keep the
* always-fresh codec path.
*
* A registry+bus instance REQUIRES an L2 replication mirror: the reader's only safe way to
* clear a bus gap is to resync off the `state`/`delta` mirror there. Constructing
* it without one is rejected LOUDLY here (dead-branch ruling) — the reader has no
* honest job with no store. This throw is dead today (a registry+bus config is only ever wired
* with L2), and it is guarded precisely because that reachability would change silently if L2
* became optional: the old handling — clearing suspicion without resyncing — was a fail-OPEN
* hole. Fail loud instead of silently reopening it. */
function buildInvalidation(config) {
	const { registry, bus, l2 } = config;
	if (!registry || !bus) return;
	if (!l2) throw new Error("createCache: a registry+bus instance requires an l2 store (the §4.2 replication mirror the reader resyncs off — E20)");
	const emit = (event) => {
		emitTelemetry(config, event);
	};
	const heartbeat = config.defaults?.heartbeatInterval;
	return new Invalidation(registry, l2, bus, heartbeat === void 0 ? void 0 : durationMs(heartbeat), emit);
}
/**
* A facade → the instance it fronts. THE ONE PLACE an alternate identity for one cache is recorded.
*
* WHY THIS EXISTS, AND WHY IT IS ONE MECHANISM RATHER THAN A HABIT. Several internal seams hang
* per-instance state off a `Cache` through a module-private WeakMap, keyed by the object itself. A
* composition that returns a WRAPPER around a `createCache` instance — `createWorkersCache` does, to
* hang its freshness carrier on the read path — hands callers an object those maps have never heard
* of. The address a seam is keyed by is therefore the identity CALLERS HOLD, and a facade that
* re-registers one seam and forgets another does not fail loudly: it produces a seam that silently
* answers `undefined` forever. Registering per-seam is the fix that does not generalize — the next
* seam, or the next facade, reopens it.
*
* So identity is aliased ONCE, here, and every seam lookup resolves through `resolveCacheIdentity`
* BEFORE keying. A new seam is covered because it resolves like the others; a new facade is covered
* because it aliases like the others. Neither has to remember the list.
*/
var identityAliases = /* @__PURE__ */ new WeakMap();
/**
* The instance `cache` stands for — itself, unless it is a facade over another.
*
* SAME-PACKAGE internal seam, NOT public API (not re-exported from `index.ts`). Every WeakMap keyed
* by a `Cache` resolves through this before keying, so a facade and the instance it fronts address
* ONE slot. A cache that is nobody's facade resolves to itself, which is every raw `createCache`
* instance and therefore the overwhelmingly common path.
*
* ONE STEP IS ENOUGH because `aliasCacheIdentity` flattens the chain at registration.
*/
function resolveCacheIdentity(cache) {
	return identityAliases.get(cache) ?? cache;
}
/** Cache instance → its replication poller. Module-private internal state: the
* poller is attached WITHOUT touching the public `Cache` surface. the request-piggyback driver
* (the RR adapter) is the READER of this map — the seam arrived with its consumer, so the map is no
* longer write-only and the `sonarjs/no-unused-collection` suppression that carried the named "C4"
* expiry is DELETED (its premise is gone; a suppression outliving its premise is a lie). */
var replicationPollers = /* @__PURE__ */ new WeakMap();
/**
* The replication poller attached to `cache`, or `undefined` for a registry-absent instance (which
* has no reader to drive). SAME-PACKAGE internal seam, NOT public API: it is deliberately not
* re-exported from `index.ts`, and its ONE consumer is the request-piggyback tick driver in the
* RR adapter (`src/adapters/react-router/poll-driver.ts`), which owns the staleness gate + the
* failure swallow. Kept as narrow as that consumer needs: a `Cache` in, its poller out — no other
* kernel internal crosses the boundary.
*
* Resolves identity first, so a facade over an instance finds that instance's poller. Without that
* an RR app on a wrapping factory would lose its replication poll SILENTLY — `tickIfStale` reads a
* missing poller as "registry-absent, nothing to drive" and returns.
*/
function replicationPollerOf(cache) {
	return replicationPollers.get(resolveCacheIdentity(cache));
}
/** A `RenderCollector` → its internal scope-bearing writer. Module-private: the
* public collector interface cannot carry the scope half (a public `dependsOn` has no entry
* behind it), so the kernel's automatic hit/fill recording reaches it through this map
* instead of through a widened public type or a cast. */
var collectorStates = /* @__PURE__ */ new WeakMap();
/**
* An `L3Emission` → how many dependencies the render had consumed when that emission was
* committed — hits, fills, and explicit `dependsOn` declarations. Same
* module-private-WeakMap seam as `collectorStates` above, and for a kindred reason: the count is
* a fact the ADAPTER's header decision needs and no caller of the public type does, and
* `L3Emission` is a spec-verbatim shape — so the count rides the private channel rather
* than widening a published surface with a field only this package reads.
*/
var emissionCounts = /* @__PURE__ */ new WeakMap();
/** Bank an emission's consumed-dependency count on the seam and return the emission (the ONE
* writer of `emissionCounts` — the count is banked with the emission it belongs to, so the two
* can never drift apart). */
function withManagedDependencies(emission, count) {
	emissionCounts.set(emission, count);
	return emission;
}
/**
* How many dependencies the render had consumed when `emission` was committed.
* SAME-PACKAGE internal seam, NOT public API: deliberately not re-exported from `index.ts`, and
* its ONE consumer is the React-Router middleware's header decision, where ZERO is the adapter's
* JURISDICTION FLOOR — a render that consumed no managed dependency gets a safe DEFAULT and
* nothing more (`private` filled in only where the app wrote no `Cache-Control`, an app-set
* `Cache-Tag` left where it is, nothing overridden). It is what separates that floor from an
* eligible render whose dependencies simply emit no tag.
*
* An emission this kernel never minted reads as 0, i.e. as the floor — the direction in which the
* adapter asserts least, which is the safe one for an unrecognized input.
*/
function managedDependenciesOf(emission) {
	return emissionCounts.get(emission) ?? 0;
}
/**
* A `Cache` → the accessor that yields the AMBIENT render collector for the request in
* flight. Same module-private-WeakMap seam as `replicationPollers` above, and the
* same reason: the collector must reach the kernel WITHOUT widening the public `Cache`
* surface and WITHOUT the kernel importing `node:async_hooks` (the request frame is the
* adapter's — a Workers deployment without the ALS compat flag must still be able to import
* this module).
*
* Keyed by RESOLVED identity, like every other instance-keyed seam here: an adapter binds against
* the cache the app holds, and the kernel looks up against the instance running the read. Those are
* different objects whenever a facade is in play, and this map is where they have to meet — a
* privacy gate that reads an empty collector concludes the render consumed nothing and asserts
* nothing, which is the loudest possible silence.
*/
var renderCollectors = /* @__PURE__ */ new WeakMap();
/**
* Bind the adapter's ambient-collector accessor to `cache`. SAME-PACKAGE internal
* seam, NOT public API: deliberately not re-exported from `index.ts`, and its ONE consumer is
* the React-Router middleware (`src/adapters/react-router/middleware.ts`), which owns the
* `AsyncLocalStorage` request frame the accessor reads.
*
* The consequence is the safety property, not a limitation: a cache used WITHOUT the adapter
* has no bound accessor, so the kernel records nothing, no `commitHeaders` ever runs, and no
* header is ever emitted. Nothing to leak, by construction — it fails closed.
*/
function bindRenderCollector(cache, accessor) {
	renderCollectors.set(resolveCacheIdentity(cache), accessor);
}
/** The ambient collector's internal writer for `cache`, or `undefined` when the adapter bound
* none / no request frame is open. */
function ambientCollectorOf(cache) {
	const collector = renderCollectors.get(resolveCacheIdentity(cache))?.();
	return collector === void 0 ? void 0 : collectorStates.get(collector);
}
/** Build the tick-driven replication poll driver alongside the reader, under the
* SAME condition (registry+bus+L2, already validated by `buildInvalidation`). It is CONSTRUCTED
* here — never a knip entry: the construction makes `replication-poller.ts` reachable, so deleting
* this wiring reds knip, which is the point (the dead-code gate becomes the wiring's tooth). The
* poller runs no timer of its own; C4 drives it (deterministic ticks drive it in tests). The
* `maxSyncLag` base cadence defaults to the Workers profile (60s) unless config overrides. */
function buildReplicationPoller(config, reader) {
	const { l2, registry } = config;
	if (!l2 || !registry) throw new Error("createCache: the replication poller requires the same registry+l2 as the reader (§4.2)");
	const maxSyncLag = config.defaults?.maxSyncLag;
	const options = maxSyncLag === void 0 ? {} : { pollBaseMs: durationMs(maxSyncLag) };
	return new ReplicationPoller(reader, l2, mirrorKeys(registry.regId), config.rng, options);
}
/**
* Register this instance's retention lease — DEFERRED TO FIRST USE, best-effort
* and fire-and-forget; a registration failure never blocks anything (the GC horizon extends under
* uncertainty).
*
* IT USED TO FIRE FROM `createCache`, AND THAT IS AN RPC AT CONSTRUCTION. Construction performs no
* I/O: at module scope the platform refuses the call outright, and the bare `try{}catch{}` around it
* — swallowing by design, which is right for a best-effort lease — meant the refusal was invisible.
* A lease that never registered and never said so is the worst of both: the Coordinator's GC horizon
* is not extended by an instance it never heard from, and nothing anywhere reports it.
*
* Returns a function the first read calls. It runs AT MOST ONCE per instance and never blocks: the
* lease is a hint to the GC horizon, not a precondition for reading, so making a read wait on it
* would trade a correctness-neutral registration for real latency on the request that happened to be
* first.
*/
function retentionRegistrar(config) {
	const { registry } = config;
	const retention = config.defaults?.maxEntryRetention;
	if (!registry || retention === void 0) return () => {};
	let registered = false;
	return () => {
		if (registered) return;
		registered = true;
		(async () => {
			try {
				await registry.registerRetention(durationMs(retention));
			} catch {}
		})();
	};
}
/** Resolve a purge selector to its registry tag(s). A key selector maps to
* the reserved `__k` tag digest of that key's canonical form — the
* IDENTICAL digest `expire`/`delete` by tag would never see, since the literal
* `__k` form is internal and only `expire({key})`/`delete({key})` may target
* it. No `scope` and no request on the key selector ⇒ the same `pub` default
* a scope-less, contextless `getOrSet` call stamps, so
* `expire({key: options.key})` targets exactly the tag a matching contextless
* `getOrSet({key: options.key})` carries. */
function selectorTags(selector, config, nsVersion) {
	if ("tag" in selector) return [selector.tag];
	return [keyTag(canonicalKey(nsVersion.current, config.namespace, scopeLiteral(), selector.key))];
}
/** Wrap a registry ack into a `PurgeResult`. `flushed`/`edgePurged`
* resolve minimally here — the registry mutation already durably accepted + emitted
* the __delta/__state batch (steps 1–3); the KV-acceptance handoff and CDN
* drain (step 4) are deferred to a later cluster. */
function purgeResult(ack) {
	return {
		edgePurged: async () => {
			await Promise.resolve();
		},
		epoch: ack.epoch,
		flushed: async () => {
			await Promise.resolve();
		},
		matchedHint: "unknown"
	};
}
/**
* Build a cache instance. Wires the dual-epoch invalidation core when a
* registry + bus are present; otherwise runs the registry-absent codec path.
* Not-yet-implemented verbs still throw `NotImplementedError`.
*
* CONSTRUCTION PERFORMS NO I/O. What happens here is bookkeeping over the arguments —
* addresses are recorded, components are pointed at each other, and no door is opened. Every
* deferred piece (the bus subscription via `Invalidation`'s lazy dial; the retention lease via
* `registerRetention` below) waits for the first read that needs it and runs inside that read's
* request context. This is what makes one cache per isolate, built at module scope, a legal and
* expected shape rather than an isolate that refuses to start.
*/
function createCache(config) {
	const inval = buildInvalidation(config);
	const poller = inval === void 0 ? void 0 : buildReplicationPoller(config, inval);
	/** The lease, armed but NOT fired — the first read pays it, and only the first. */
	const registerRetention = retentionRegistrar(config);
	const nsVersion = new NsVersion();
	const inFlight = newSingleflight();
	const ambientCell = { lookup() {} };
	const ambient = () => ambientCell.lookup();
	const instance = {
		async clear() {
			const { registry } = config;
			if (!registry) return notImplemented("cache.clear (no registry configured)", config);
			inval?.ensureSubscribed();
			nsVersion.bump();
			return purgeResult(await registry.delete([nsTag(config.namespace)]));
		},
		collect() {
			const required = [];
			let committed = false;
			let lateTag = false;
			let managedDependencies = 0;
			let poisoned = false;
			let scopeUnreadable = false;
			const records = [];
			const state = {
				get records() {
					return records;
				},
				record(dependency) {
					records.push(dependency);
					const { l3, scope, tags } = dependency;
					managedDependencies += 1;
					if (scope.kind === "unreadable") {
						scopeUnreadable = true;
						poisoned = true;
					} else if (scope.kind === "readable" && scope.literal !== PUBLIC_SCOPE) poisoned = true;
					if (l3 === false) return;
					if (committed) {
						lateTag = true;
						return;
					}
					required.push(...tags);
				}
			};
			const collector = {
				commitHeaders(budget) {
					const cacheTags = required.filter((tag) => !isReservedTag(tag));
					committed = true;
					if (poisoned) return withManagedDependencies({
						cacheTags: [],
						eligible: false,
						ineligibleReason: scopeUnreadable ? "scope-unreadable" : "scope"
					}, managedDependencies);
					if (lateTag) return withManagedDependencies({
						cacheTags,
						eligible: false,
						ineligibleReason: "late-tag"
					}, managedDependencies);
					const maxTags = budget?.maxTags;
					if (maxTags !== void 0 && cacheTags.length > maxTags) return withManagedDependencies({
						cacheTags,
						eligible: false,
						ineligibleReason: "budget"
					}, managedDependencies);
					const maxBytes = budget?.maxBytes;
					if (maxBytes !== void 0) {
						if (new TextEncoder().encode(cacheTags.join(",")).length > maxBytes) return withManagedDependencies({
							cacheTags,
							eligible: false,
							ineligibleReason: "budget"
						}, managedDependencies);
					}
					return withManagedDependencies({
						cacheTags,
						eligible: true
					}, managedDependencies);
				},
				dependsOn(tag, dependsOptions) {
					state.record({
						scope: { kind: "none" },
						tags: [tag],
						...dependsOptions?.l3 === void 0 ? {} : { l3: dependsOptions.l3 }
					});
				}
			};
			collectorStates.set(collector, state);
			return collector;
		},
		async delete(selector) {
			const { registry } = config;
			if (!registry) return notImplemented("cache.delete (no registry configured)", selector);
			inval?.ensureSubscribed();
			return purgeResult(await registry.delete(selectorTags(selector, config, nsVersion)));
		},
		/** Deferred to v0.4 — namespace-wide hard purge behind the origin-load guard. */
		async deleteAll(guard) {
			return notImplemented("cache.deleteAll (deferred to v0.4)", config, guard);
		},
		async expire(selector) {
			const { registry } = config;
			if (!registry) return notImplemented("cache.expire (no registry configured)", selector);
			inval?.ensureSubscribed();
			return purgeResult(await registry.expire(selectorTags(selector, config, nsVersion)));
		},
		/** Deferred to v0.4 — namespace-wide soft purge behind the origin-load guard. */
		async expireAll(guard) {
			return notImplemented("cache.expireAll (deferred to v0.4)", config, guard);
		},
		/**
		* Witness a key's state — introspection, not testimony: every field is drawn from
		* an ALREADY-KNOWN source, and nothing here mutates state. The tier is a NON-mutating presence
		* probe (`store.get`, never `hydrateL1` ); the verdict is a pure classify over the
		* reader's CURRENT knowledge (no live check, no resync ); the reader state is exposed
		* via read-only accessors; the request attribution is the bound render collector's
		* request-scoped records. The key is canonicalized at the DEFAULT (`pub`) scope — the
		* signature carries no scope, so `explain` introspects the default-scoped entry for that key.
		*/
		async explain(key) {
			const codec = codecOf(config);
			const canonical = canonicalKey(nsVersion.current, config.namespace, scopeLiteral(), key);
			const reader = {
				appliedEpoch: inval?.appliedEpoch ?? 0,
				channel: inval?.channel ?? "never-established",
				suspect: inval?.suspect ?? false,
				terminal: poller?.terminal ?? false
			};
			const requestDependencies = explainAttribution(ambient);
			const attribution = requestDependencies === void 0 ? {} : { requestDependencies };
			let readFailed = false;
			for (const [store, tier] of tiersOf(config)) {
				const probed = await probeStoredEntry(store, codec, config, canonical);
				if (probed.kind !== "found") {
					readFailed ||= probed.kind === READ_FAILED_KIND;
					continue;
				}
				return {
					identity: {
						bornEpoch: probed.envelope.bornEpoch,
						kind: probed.envelope.kind,
						scope: probed.scope,
						tags: probed.envelope.tags,
						ttl: probed.ttl
					},
					key,
					kind: "present",
					reader,
					tier,
					verdict: inval ? inval.explainVerdict(probed.envelope) : FRESH_VERDICT,
					...attribution
				};
			}
			if (readFailed) return {
				key,
				kind: READ_FAILED_KIND,
				reader,
				...attribution
			};
			return {
				key,
				kind: "absent",
				reader,
				tier: "miss",
				...attribution
			};
		},
		async getOrSet(options) {
			registerRetention();
			inval?.ensureSubscribed();
			const outcome = await runGetOrSet(config, options, inval, nsVersion, "getOrSet", inFlight, ambient);
			if (outcome.value === void 0 && outcome.tier === "miss") {
				if (outcome.error !== void 0) throw outcome.error;
				throw new FencedError(options.key);
			}
			return outcome.value;
		},
		async getOrSetEntry(options) {
			registerRetention();
			inval?.ensureSubscribed();
			const outcome = await runGetOrSet(config, options, inval, nsVersion, "getOrSetEntry", inFlight, ambient);
			return {
				age: outcome.bornMs === void 0 ? 0 : Math.max(0, config.clock.now() - outcome.bornMs),
				...outcome.durable === void 0 ? {} : { durable: outcome.durable },
				skipped: outcome.skipped,
				stale: outcome.stale,
				tier: outcome.tier,
				value: outcome.value,
				...outcome.servedOnError === void 0 ? {} : { servedOnError: outcome.servedOnError }
			};
		}
	};
	ambientCell.lookup = () => ambientCollectorOf(instance);
	if (poller !== void 0) replicationPollers.set(instance, poller);
	return instance;
}
/**
* The counter a swallowed tick failure increments. The tick runs OUT-OF-BAND from the response,
* so its failure must never surface to the caller — but a silently failing replication poll is
* exactly the kind of degradation an operator has to be able to see, so the swallow is paired with
* this event. Deliberately payload-free apart from the error's NAME (a code, never free-form detail
* or a key/tag reference), so a plain sink can be pointed at it safely.
*
* NOTE: `registry_degraded` / `state_stale` — the richer telemetry names this event may
* eventually be folded into — are now ENROLLED in the const-union catalog (`TELEMETRY_EVENTS`) as
* RESERVED members, but their emit condition is unruled: `poll_tick_failed` keeps emitting as-is.
* (FOR FABLE: whether to enrich/rename this counter into a degraded-mode signal is an open question.)
*/
var POLL_TICK_FAILED = TELEMETRY_EVENTS.poll_tick_failed;
/** Last piggybacked tick per cache instance — the staleness gate's cursor. Per-isolate by nature
* (an isolate holds one `Cache`), and keyed by the instance so two caches in one isolate never
* starve each other's ticks.
*
* Keyed by RESOLVED identity (the third instance-keyed seam, and the one that does not live beside
* the other two). A facade and the instance it fronts drive the SAME poller, so they must share the
* SAME gate: two gates over one poller would let an app holding both objects tick it twice per
* window, which is exactly the hammering the gate exists to stop. */
var lastTickMs = /* @__PURE__ */ new WeakMap();
/**
* THE SWALLOW WRAP (adapter-side; the guard itself now lives in the shared `safeEmit`).
* `ctx.waitUntil(tick())` on a rejecting promise surfaces as an unhandled rejection in the request's
* lifecycle — a failing L2 read would then be able to reach the response of a request that never
* needed that read. So the tick is wrapped: catch → count → swallow. Adapter code by ruling, not
* kernel: the kernel's `resync` is entitled to reject, and the driver that chose to run it
* out-of-band is what owes the caller its silence.
*
* The returned promise NEVER rejects — and the guarantee is AIRTIGHT over the whole failure surface,
* INCLUDING the reporting channel itself: the telemetry emit is routed through `safeEmit`, so
* neither a throwing sink nor a rejecting `async` sink can turn the swallow into an escape, and the
* injected `onSinkError` hook observes a swallowed sink failure. (The default `waitUntil` drops
* the promise on the floor; a rejection here would therefore become an unhandled rejection —
* process-fatal on the Node/RR lane, an uncaught request error on workerd. A background freshness
* probe must never be able to kill a user's request.)
*/
async function runTick(poller, nowMs, telemetry, onSinkError) {
	try {
		await poller.onPollTick(nowMs);
	} catch (error) {
		safeEmit(telemetry, {
			error: error instanceof Error ? error.name : "unknown",
			type: POLL_TICK_FAILED
		}, onSinkError);
	}
}
/**
* Fire one replication poll tick for `cache` IF the last one is stale (see `TICK_MIN_INTERVAL_MS`).
*
* Returns the in-flight tick — hand it straight to `ctx.waitUntil` so it outlives the response — or
* `undefined` when nothing is due (the gate held, or the cache is registry-absent and has no reader
* to drive). The returned promise NEVER rejects: a store/resync failure inside the tick is caught,
* counted through `telemetry` (`poll_tick_failed`), and swallowed, because a background freshness
* probe has no business failing a user's response. The `telemetry` sink is NOT trusted to keep that
* promise: it is app code on the failure path, so a sink that throws — or an `async` sink that
* rejects — is neutralized too, and floats nothing.
*/
function tickIfStale(cache, nowMs, telemetry, onSinkError) {
	const poller = replicationPollerOf(cache);
	if (poller === void 0) return;
	const gateKey = resolveCacheIdentity(cache);
	const last = lastTickMs.get(gateKey);
	if (last !== void 0 && nowMs - last < 1e3) return;
	lastTickMs.set(gateKey, nowMs);
	return runTick(poller, nowMs, telemetry, onSinkError);
}
//#endregion
//#region ../../packages/cache/src/adapters/react-router/middleware.ts
/** The single telemetry event a shared-cache-INELIGIBLE response emits — exactly
* one per response, naming the reason CLASS in its `reason` field and nothing else. No tag names
* ride the payload: which dependency did it is a question for `explain()`, and an operator
* watching this counter only needs to see THAT a route stopped being CDN-cacheable. */
var L3_INELIGIBLE = TELEMETRY_EVENTS.l3_ineligible;
/**
* The shared-cache header budget: 16 KB aggregate, ~1,000 tags. (The 1,024-char cap is a
* purge-API limit, not a header one — header tags carry no per-tag cap.) Overridable per
* middleware via `CacheMiddlewareOptions.l3Budget`.
*/
var L3_BUDGET_DEFAULT = {
	maxBytes: 16 * 1024,
	maxTags: 1e3
};
/**
* The reason CLASS a telemetry consumer sees, mapped from the kernel's `L3Ineligibility` union.
* TWO VOCABULARIES, ONE MAPPING POINT — deliberately: the kernel's union is kebab (its own existing
* convention, `late-tag`), while the event vocabulary is snake (`poll_tick_failed`,
* `singleflight_option_mismatch`), and the design fixes `scope_unreadable` as the literal class name. This
* function is the ONLY place the two meet; nothing downstream re-spells either.
*/
var REASON_CLASS = {
	budget: "budget",
	"late-tag": "late_tag",
	scope: "scope",
	"scope-unreadable": "scope_unreadable"
};
/** The two response headers this adapter governs. `Cache-Control` carries the shared-cache
* posture; `Cache-Tag` is the CDN purge handle keyed off the render's dependency tags. */
var CACHE_CONTROL = "Cache-Control";
var CACHE_TAG = "Cache-Tag";
/** The one directive the adapter ever WRITES to `Cache-Control`. It never writes
* `public` or an `s-maxage` — opting into shared caching is the application's call, and the
* adapter's job is to emit the purge tags when that is safe and to demote when it is not. */
var PRIVATE = "private";
/**
* The `RouterContext<Cache>` key the middleware writes the cache under. A loader/action reads the
* request's cache with `context.get(cacheContext)`. Created with no default value, so a `get` in a
* request the middleware never ran on throws (an honest misconfiguration signal, not a silent miss).
*/
var cacheContext = createContext();
/** The empty principal-bearing frame used when no `request` derivation is configured — a request
* with no adapter-visible identity: `derivePrincipal` finds none ⇒ the `pub` class. */
var EMPTY_REQUEST_CONTEXT = Object.freeze({});
var requestFrame = new AsyncLocalStorage$1();
var collectorFrame = new AsyncLocalStorage$1();
/**
* The current request's principal-bearing `RequestContext`, or `undefined` outside a middleware
* frame. Pass it straight through to `getOrSet`'s `request` option so the kernel derives the
* request's scope: `cache.getOrSet({ key, request: currentRequest(), factory })`.
*/
function currentRequest() {
	return requestFrame.getStore();
}
/** The `waitUntil` used when the app configures none: the tick still runs, it is simply not held
* alive past the response (best-effort, and safe — the driver's promise never rejects). */
function floatingWaitUntil(promise) {}
/** Nothing to write — the app's headers stand exactly as authored. */
var NO_EDIT = Object.freeze({});
/**
* Decide the posture for the response, given the render's emission and how many
* dependencies that render consumed (`managed` — hits, fills, and explicit `dependsOn`
* declarations, read off the kernel's in-package seam; it is deliberately NOT a field of the
* spec-verbatim public `L3Emission`, which stays exactly the shape names).
*
* THREE postures, in precedence order:
*
*   1. NO MANAGED DEPENDENCY (the jurisdiction floor). The render consumed nothing this library
*      manages, so the adapter asserts nothing: it fills in `Cache-Control: private` ONLY where the
*      application set no cache header at all, and it leaves an app-set `Cache-Tag` alone. A
*      default-fill, never an override — the adapter has observed nothing that licenses an opinion.
*      This is also what separates the floor from an ELIGIBLE render that happens to emit no tag:
*      the emission alone cannot tell those apart, and they get different postures.
*
*   2. INELIGIBLE (scope-poisoned, or late-tag/budget). `Cache-Control: private` is FORCED — it
*      overwrites whatever the application set, because the application's own header is what has to
*      lose here: the whole point of the branch is a route that asked to be publicly cached while
*      its render read per-principal content. Any `Cache-Tag` is DELETED, including one the adapter
*      never assembled: removing it, rather than declining to add one, is what the branch is for —
*      a downstream cache parses the header off the wire whether or not it stores the response.
*
*   3. ELIGIBLE. The adapter's own assembled `Cache-Tag` replaces whatever was there — DEDUPLICATED
*      here, at the join. One entry read by several loaders in a render reaches the collector once
*      per consumption, so its tag arrives once per consumption; a header that repeats the same
*      purge handle buys nothing and costs bytes. The dedup lives HERE and NOT in the collector on
* purpose: `commitHeaders`'s BUDGET counts every occurrence (that is the measure invariant
*      13 pins — collapsing duplicates there would make an over-budget tag list come out eligible),
*      and emitting fewer bytes than were measured is always safe, while measuring fewer bytes than
*      are emitted is not. The adapter does NOT fabricate public caching — no `public`, no
*      `s-maxage`, ever. The application opts into shared caching with its own `Cache-Control`; the
*      adapter's job is to emit the purge tags when that is safe and to demote when it is not. So
*      the safe `private` default still fills in where the app wrote no cache header.
*/
function decidePosture(emission, managed, appSetCacheControl) {
	const safeDefault = appSetCacheControl ? NO_EDIT : { cacheControl: PRIVATE };
	if (managed === 0) return safeDefault;
	if (!emission.eligible) return {
		cacheControl: PRIVATE,
		cacheTag: null
	};
	const emitted = [...new Set(emission.cacheTags)];
	return {
		...safeDefault,
		cacheTag: emitted.length > 0 ? emitted.join(",") : null
	};
}
/** Write one decided posture onto a mutable `Headers`. Throws iff the guard is immutable — which is
* exactly the signal `applyHeaders` acts on, and never a partial write: an immutable guard rejects
* the FIRST mutation, so either the whole posture lands or none of it does. */
function writePosture(headers, posture) {
	if (posture.cacheControl !== void 0) headers.set(CACHE_CONTROL, posture.cacheControl);
	if (posture.cacheTag === null) headers.delete(CACHE_TAG);
	else if (posture.cacheTag !== void 0) headers.set(CACHE_TAG, posture.cacheTag);
}
/**
* Rebuild `response` with a MUTABLE header guard and the posture written onto it, preserving status,
* statusText, body, and every header the original carried (the `Response(body, response)` init form
* copies all three). `undefined` when the platform refuses to reconstruct the response at all — see
* `applyHeaders` for why that residue is safe.
*/
function rebuiltWithPosture(response, posture) {
	try {
		const rebuilt = new Response(response.body, response);
		writePosture(rebuilt.headers, posture);
		return rebuilt;
	} catch {
		return;
	}
}
/**
* Apply the posture to the response and return the response to send.
*
* NOT ALL RESPONSES ARE WRITABLE. A `Response` obtained from `Response.redirect(...)`, from a
* `fetch()` passthrough, or from `caches.match(...)` carries an IMMUTABLE header guard: every
* `set`/`delete` on it throws `TypeError`. A middleware that mutates headers unconditionally
* therefore rejects its own promise on such a response and turns it into a 500 — a route that
* merely redirects would break. So the write is attempted, and on refusal the SAME decided posture
* is written onto a rebuilt copy (status, statusText, body and headers all preserved) which is
* returned in the original's place.
*
* THE RESIDUE, and why it is safe: a response the platform will not let us reconstruct either
* (`Response.error()` and the other sub-200 network-error forms) is returned untouched. Such a
* response carries NO readable or writable headers at all — there is no app-set `Cache-Control` to
* override and no `Cache-Tag` for a shared cache to read off it — so declining to write a posture
* onto it withholds nothing and leaks nothing. The one thing that must never happen — a throw out
* of the middleware — cannot.
*
* The telemetry event an ineligible response owes is emitted exactly once, off the
* DECISION, so it is independent of which of the three write paths the response takes: one event per
* response, naming the reason CLASS, carrying no tag names.
*/
function applyHeaders(response, emission, telemetry, onSinkError) {
	const managed = managedDependenciesOf(emission);
	const posture = decidePosture(emission, managed, response.headers.has(CACHE_CONTROL));
	if (managed > 0 && !emission.eligible && emission.ineligibleReason !== void 0) safeEmit(telemetry, {
		reason: REASON_CLASS[emission.ineligibleReason],
		type: L3_INELIGIBLE
	}, onSinkError);
	if (posture === NO_EDIT) return response;
	try {
		writePosture(response.headers, posture);
		return response;
	} catch {}
	return rebuiltWithPosture(response, posture) ?? response;
}
/**
* Build the RR-v8 root middleware. Always-on in v8 (the `unstable_`/`v8_middleware` flag
* was removed at 8.0.0): register the returned function in a route module's `middleware` array. It
* provides the cache into `context`, opens the request + collector frames, fires the replication
* poll tick (unawaited), `await`s `next()`, commits the collector, and applies the
* header algorithm.
*/
function cacheMiddleware(options) {
	const { cache, l3Budget = L3_BUDGET_DEFAULT, onSinkError, request: deriveRequest, telemetry, waitUntil = floatingWaitUntil } = options;
	bindRenderCollector(cache, () => collectorFrame.getStore());
	return async (args, next) => {
		const requestContext = deriveRequest?.(args) ?? EMPTY_REQUEST_CONTEXT;
		const collector = cache.collect();
		return await requestFrame.run(requestContext, async () => {
			args.context.set(cacheContext, cache);
			const tick = tickIfStale(cache, Date.now(), telemetry, onSinkError);
			if (tick !== void 0) waitUntil(tick);
			return await collectorFrame.run(collector, async () => {
				const response = await next();
				if (response instanceof Response) return applyHeaders(response, collector.commitHeaders(l3Budget), telemetry, onSinkError);
				return response;
			});
		});
	};
}
//#endregion
//#region app/cache.ts
var cache = createCache({
	namespace: "rr-fixture",
	clock: { now: () => Date.now() },
	rng: { next: () => Math.random() }
});
//#endregion
//#region app/root.tsx
var root_exports = /* @__PURE__ */ __exportAll({
	Layout: () => Layout,
	default: () => root_default,
	middleware: () => middleware
});
var middleware = [cacheMiddleware({ cache })];
function Layout({ children }) {
	return /* @__PURE__ */ (0, import_jsx_runtime.jsxs)("html", {
		lang: "en",
		children: [/* @__PURE__ */ (0, import_jsx_runtime.jsxs)("head", { children: [
			/* @__PURE__ */ (0, import_jsx_runtime.jsx)("meta", { charSet: "utf-8" }),
			/* @__PURE__ */ (0, import_jsx_runtime.jsx)("meta", {
				name: "viewport",
				content: "width=device-width, initial-scale=1"
			}),
			/* @__PURE__ */ (0, import_jsx_runtime.jsx)(Meta, {}),
			/* @__PURE__ */ (0, import_jsx_runtime.jsx)(Links, {})
		] }), /* @__PURE__ */ (0, import_jsx_runtime.jsxs)("body", { children: [
			children,
			/* @__PURE__ */ (0, import_jsx_runtime.jsx)(ScrollRestoration, {}),
			/* @__PURE__ */ (0, import_jsx_runtime.jsx)(Scripts, {})
		] })]
	});
}
var root_default = withComponentProps(function App() {
	return /* @__PURE__ */ (0, import_jsx_runtime.jsx)(Outlet, {});
});
//#endregion
//#region app/routes/home.tsx
var home_exports = /* @__PURE__ */ __exportAll({
	default: () => home_default,
	loader: () => loader
});
async function loader({ context }) {
	return { greeting: await context.get(cacheContext).getOrSet({
		key: "home:greeting",
		request: currentRequest(),
		factory: async () => "hello from the @astilba/cache RR fixture"
	}) };
}
var home_default = withComponentProps(function Home() {
	return /* @__PURE__ */ (0, import_jsx_runtime.jsx)("h1", { children: "@astilba/cache — React Router v8 build-smoke fixture" });
});
//#endregion
//#region \0virtual:react-router/server-manifest
var server_manifest_default = {
	"entry": {
		"module": "/assets/entry.client-E4jjLjTF.js",
		"imports": ["/assets/jsx-runtime-DTqGV-ts.js", "/assets/errorBoundaries-loDjjY7F.js"],
		"css": []
	},
	"routes": {
		"root": {
			"id": "root",
			"parentId": void 0,
			"path": "",
			"index": void 0,
			"caseSensitive": void 0,
			"hasAction": false,
			"hasLoader": false,
			"hasClientAction": false,
			"hasClientLoader": false,
			"hasClientMiddleware": false,
			"hasDefaultExport": true,
			"hasErrorBoundary": false,
			"module": "/assets/root-CaRPDHLn.js",
			"imports": ["/assets/jsx-runtime-DTqGV-ts.js", "/assets/errorBoundaries-loDjjY7F.js"],
			"css": [],
			"clientActionModule": void 0,
			"clientLoaderModule": void 0,
			"clientMiddlewareModule": void 0,
			"hydrateFallbackModule": void 0
		},
		"routes/home": {
			"id": "routes/home",
			"parentId": "root",
			"path": void 0,
			"index": true,
			"caseSensitive": void 0,
			"hasAction": false,
			"hasLoader": true,
			"hasClientAction": false,
			"hasClientLoader": false,
			"hasClientMiddleware": false,
			"hasDefaultExport": true,
			"hasErrorBoundary": false,
			"module": "/assets/home-CkfepAqz.js",
			"imports": ["/assets/jsx-runtime-DTqGV-ts.js"],
			"css": [],
			"clientActionModule": void 0,
			"clientLoaderModule": void 0,
			"clientMiddlewareModule": void 0,
			"hydrateFallbackModule": void 0
		}
	},
	"url": "/assets/manifest-f916a1ce.js",
	"version": "f916a1ce",
	"sri": void 0
};
//#endregion
//#region \0virtual:react-router/server-build
var assetsBuildDirectory = "build/client";
var basename = "/";
var future = {
	"unstable_enableNodeReadableStream": false,
	"unstable_optimizeDeps": false
};
var ssr = true;
var isSpaMode = false;
var prerender = [];
var routeDiscovery = {
	"mode": "lazy",
	"manifestPath": "/__manifest"
};
var publicPath = "/";
var entry = { module: entry_server_web_exports };
var routes = {
	"root": {
		id: "root",
		parentId: void 0,
		path: "",
		index: void 0,
		caseSensitive: void 0,
		module: root_exports
	},
	"routes/home": {
		id: "routes/home",
		parentId: "root",
		path: void 0,
		index: true,
		caseSensitive: void 0,
		module: home_exports
	}
};
var allowedActionOrigins = false;
//#endregion
export { allowedActionOrigins, server_manifest_default as assets, assetsBuildDirectory, basename, entry, future, isSpaMode, prerender, publicPath, routeDiscovery, routes, ssr };
