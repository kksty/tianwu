import{P as e,j as s,v as a}from"./index-CeqoK2vL.js";
/**
 * @license lucide-react v0.400.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const l=e("LoaderCircle",[["path",{d:"M21 12a9 9 0 1 1-6.219-8.56",key:"13zald"}]]),t={sm:"h-4 w-4",md:"h-6 w-6",lg:"h-8 w-8"};function n({size:e="md",className:n,label:i}){return s.jsxs("div",{className:"flex items-center justify-center gap-2",role:"status",children:[s.jsx(l,{className:a("animate-spin text-primary",t[e],n)}),i&&s.jsx("span",{className:"text-ui text-ink-2",children:i}),s.jsx("span",{className:"sr-only",children:i||"Loading..."})]})}function i({label:e,hint:l,error:t,className:n,children:i}){return s.jsxs("label",{className:a("block space-y-1.5",n),children:[s.jsx("span",{className:"block text-[12px] font-medium leading-4 text-ink-2",children:e}),i,t?s.jsx("span",{className:"block text-[11px] leading-4 text-destructive",children:t}):l?s.jsx("span",{className:"block text-[11px] leading-4 text-ink-3",children:l}):null]})}export{i as F,n as S};
