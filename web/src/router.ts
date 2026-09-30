import { createRouter, createWebHistory } from 'vue-router'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'home', component: () => import('./views/HomeView.vue') },
    { path: '/e/:org/:slug', name: 'event', component: () => import('./views/EventView.vue'), props: true },
    { path: '/payment/return', name: 'payment-return', component: () => import('./views/PaymentReturnView.vue') },
    { path: '/orders/:id', name: 'order', component: () => import('./views/OrderView.vue'), props: true },
    { path: '/me', name: 'me', component: () => import('./views/MyTicketsView.vue') },
    { path: '/t/:token', name: 'ticket', component: () => import('./views/TicketView.vue'), props: true },
    { path: '/:pathMatch(.*)*', name: 'not-found', component: () => import('./views/NotFoundView.vue') },
  ],
  scrollBehavior(to, _from, saved) {
    if (saved) return saved
    if (to.hash) return { el: to.hash, behavior: 'smooth', top: 80 }
    return { top: 0 }
  },
})
