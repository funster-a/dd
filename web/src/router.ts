import { createRouter, createWebHistory } from 'vue-router'
import { useOrgAuth } from './composables/orgAuth'

declare module 'vue-router' {
  interface RouteMeta {
    // site — сайт покупателя с шапкой и подвалом; org — кабинет; bare — экран без обвязки.
    layout?: 'site' | 'org' | 'bare'
    title?: string
  }
}

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'home', component: () => import('./views/HomeView.vue') },
    { path: '/e/:org/:slug', name: 'event', component: () => import('./views/EventView.vue'), props: true },
    { path: '/payment/return', name: 'payment-return', component: () => import('./views/PaymentReturnView.vue') },
    { path: '/orders/:id', name: 'order', component: () => import('./views/OrderView.vue'), props: true },
    { path: '/me', name: 'me', component: () => import('./views/MyTicketsView.vue') },
    { path: '/t/:token', name: 'ticket', component: () => import('./views/TicketView.vue'), props: true },

    { path: '/scan', name: 'scan', component: () => import('./views/ScannerView.vue'), meta: { layout: 'bare', title: 'Контроль входа' } },

    {
      path: '/org/login',
      name: 'org-login',
      component: () => import('./views/org/OrgLoginView.vue'),
      meta: { layout: 'bare', title: 'Вход для организаторов' },
    },
    {
      path: '/org',
      component: () => import('./components/org/OrgLayout.vue'),
      meta: { layout: 'org' },
      children: [
        { path: '', name: 'org-events', component: () => import('./views/org/OrgEventsView.vue'), meta: { layout: 'org', title: 'События' } },
        {
          path: 'events/new',
          name: 'org-event-new',
          component: () => import('./views/org/OrgEventNewView.vue'),
          meta: { layout: 'org', title: 'Новое событие' },
        },
        {
          path: 'events/:id',
          name: 'org-event',
          component: () => import('./views/org/OrgEventView.vue'),
          props: true,
          meta: { layout: 'org', title: 'Событие' },
        },
        { path: 'venues', name: 'org-venues', component: () => import('./views/org/OrgVenuesView.vue'), meta: { layout: 'org', title: 'Площадки' } },
        {
          path: 'venues/:id',
          name: 'org-venue',
          component: () => import('./views/org/OrgVenueView.vue'),
          props: true,
          meta: { layout: 'org', title: 'Площадка' },
        },
        {
          path: 'venues/:id/seat-maps/new',
          name: 'org-seatmap-new',
          component: () => import('./views/org/OrgSeatMapNewView.vue'),
          props: true,
          meta: { layout: 'org', title: 'Новая схема зала' },
        },
      ],
    },

    { path: '/:pathMatch(.*)*', name: 'not-found', component: () => import('./views/NotFoundView.vue') },
  ],
  scrollBehavior(to, _from, saved) {
    if (saved) return saved
    if (to.hash && to.name !== 'scan') return { el: to.hash, behavior: 'smooth', top: 80 }
    return { top: 0 }
  },
})

// Кабинет только после входа: иначе — на экран входа с возвратом обратно.
router.beforeEach((to) => {
  if (to.meta.layout === 'org' && !useOrgAuth().authed.value) {
    return { name: 'org-login', query: { next: to.fullPath } }
  }
})

router.afterEach((to) => {
  if (to.meta.title) document.title = `${to.meta.title} — Партер`
})
